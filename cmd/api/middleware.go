package main

import (
	"errors"
	"expvar"
	"fmt"
	"github.com/felixge/httpsnoop"
	"github.com/tomasen/realip"
	"golang.org/x/time/rate"
	"greenlight.luismatosgarcia.dev/internal/data"
	"greenlight.luismatosgarcia.dev/internal/validator"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

func (app *application) recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Create a deferred function (which will always be run in the event of a panic as Go unwinds the stack).
		defer func() {
			// Use the builtin recover function to check if there has been a panic or not.
			if err := recover(); err != nil {
				// If there was a panic, set a "Connection close" header on the response. This acts as a trigger
				// to make Go's HTTP server automatically close the current connection after a response has been sent.
				w.Header().Set("Connection", "close")

				// The value returned by recover() has the type any, so we use fmt.Errorf() to normalize it into
				// an error and call our serverErrorResponse() helper. In turn, this will log the error using our
				// custom Logger type at the ERROR level and send the client a 500 Internal Server Error response.
				app.serverErrorResponse(w, r, fmt.Errorf("%s", err))
			}
		}()

		next.ServeHTTP(w, r)
	})
}

func (app *application) rateLimit(next http.Handler) http.Handler {
	//Define a client struct to hold the rate limiter and last seen time for each client.
	type client struct {
		limiter  *rate.Limiter
		lastSeen time.Time
	}
	// Declare a mutex and a map to hold the clients' IP addresses and rate limiters.
	var (
		mu      sync.Mutex
		clients = make(map[string]*client)
	)

	// Launch a background goroutine which removes old entries from the clients map once every minute.
	go func() {
		for {
			time.Sleep(time.Minute)

			// Lock the mutex to prevent any rate limiter checks from happening while the cleanup is taking place.
			mu.Lock()

			// Loop through all clients. If they haven't been seen within the last three minutes, delete the
			// corresponding entry from the corresponding entry from the map.
			for ip, client := range clients {
				if time.Since(client.lastSeen) > 3*time.Minute {
					delete(clients, ip)
				}
			}

			// Importantly, unlock the mutex when the cleanup is complete.
			mu.Unlock()

		}
	}()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only carry out the check if rate limiting is enabled.
		if app.config.limiter.enabled {
			// Use the realip.FromRequest() function to get the client's real IP address.
			ip := realip.FromRequest(r)

			// Lock the mutex to prevent this code from being executed concurrently.
			mu.Lock()

			// Check to see if the IP address already exists in the map. If it doesn't, then initialize a new rate limiter
			// and add the IP address and limiter to the map.
			if _, found := clients[ip]; !found {
				// Create and add a new client struct to the map if it doesn't already exist.
				clients[ip] = &client{limiter: rate.NewLimiter(rate.Limit(app.config.limiter.rps), app.config.limiter.burst)}
			}

			// Update the last seen time for the client
			clients[ip].lastSeen = time.Now()

			// Call the Allow() method on the rate limiter for the current IP address. If the request isn't allowed,
			// unlock the mutex and send a 429 Too Many Requests response.
			if !clients[ip].limiter.Allow() {
				mu.Unlock()
				app.rateLimitExceededResponse(w, r)
				return
			}

			// Very importantly unlock the mutex before calling the next handler in the chain. Notice that we DON'T
			// use defer to unlock the mutex, as would mean that the mutex isn't unlocked until all the handlers downstream
			// of this middleware have also returned.
			mu.Unlock()

		}
		next.ServeHTTP(w, r)
	})

	// Distributed Applications
	// Using this pattern for rate-limiting will only work if your API application is running on a single-machine.
	// If your infrastructure is distributed, with your application running on multiple servers behind a load
	// balancer, then you'll need to use an alternative approach.

	// If you're using HAProxy or Nginx as a load balancer or reverse proxy, both of these have built-in functionality
	// for rate limiting that it would probably be sensible to use. Alternatively, you could use a fast database
	// like Redis to maintain a request count for clients, running on a server which all your application servers
	// can communicate with.
}

func (app *application) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Add the "Vary: Authorization" header to the response. This indicates to any caches that the response
		// may vary based on the value of the Authorization header in the request.
		w.Header().Add("Vary", "Authorization")

		// Retrieve the value of the Authorization header from the request. This will return the empty string ""
		// if there is no such header found.
		authorizationHeader := r.Header.Get("Authorization")

		// If there is no Authorization header found, use the contextSetUser() helper that we just made to add the
		// AnonymousUser to the request context. Then we call the next handler in the chain and return without
		// executing any of the code below.
		if authorizationHeader == "" {
			r = app.contextSetUser(r, data.AnonymousUser)
			next.ServeHTTP(w, r)
			return
		}

		// Otherwise, we expect the value of the Authorization header to be in the format "Bearer <token>". We try
		// to split this into its constituent parts, and if the header isn't in the expected format we return a
		// 401 Unauthorized response using the invalidAuthenticationTokenResponse() helper.
		headerParts := strings.Split(authorizationHeader, " ")
		if len(headerParts) != 2 || headerParts[0] != "Bearer" {
			app.invalidCredentialsResponse(w, r)
			return
		}

		// Extract the actual authentication token from the header parts.
		token := headerParts[1]

		// Validate the token to make sure it is in a sensible format.
		v := validator.New()

		// If the token isn't valid, use the invalidAuthenticationTokenResponse() helper to send a response.
		if data.ValidateTokenPlaintext(v, token); !v.Valid() {
			app.invalidAuthenticationTokenResponse(w, r)
			return
		}

		// Retrieve the details of the user associated with the authentication token, again calling the
		// invalidAuthenticationTokenResponse() if no matching record was found. IMPORTANT: Notice that we are using
		// ScopeAuthentication as the firstparameter here.
		user, err := app.models.Users.GetForToken(data.ScopeAuthentication, token)
		if err != nil {
			switch {
			case errors.Is(err, data.ErrRecordNotFound):
				app.invalidCredentialsResponse(w, r)
			default:
				app.serverErrorResponse(w, r, err)
			}

			return
		}

		// Call the contextSetUser() helper to add the user information to the request context.
		r = app.contextSetUser(r, user)

		// Call the next handler in the chain
		next.ServeHTTP(w, r)
	})
}

func (app *application) requireActivatedUser(next http.HandlerFunc) http.HandlerFunc {
	fn := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := app.contextGetUser(r)

		// Check that a user is activated.
		if !user.Activated {
			app.inactiveAccountResponse(w, r)
			return
		}

		next.ServeHTTP(w, r)
	})

	// Wrap fn with the requiredAuthenticatedUser() middleware before returning it.
	return app.requireAuthenticatedUser(fn)
}

// Create a new requireAuthenticatedUser() middleware to check that a user is not anonymous.
func (app *application) requireAuthenticatedUser(next http.HandlerFunc) http.HandlerFunc {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := app.contextGetUser(r)

		if user.IsAnonymous() {
			app.authenticationRequiredResponse(w, r)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// Note that the first parameter for the middleware function is the permission code that we require the user to have
func (app *application) requirePermission(code string, next http.HandlerFunc) http.HandlerFunc {
	fn := func(w http.ResponseWriter, r *http.Request) {
		// Retrieve the user from the context.
		user := app.contextGetUser(r)

		// Get the slice of permissions for the user.
		permissions, err := app.models.Permissions.GetAllForUser(user.ID)
		if err != nil {
			app.serverErrorResponse(w, r, err)
			return
		}

		// Check if the slice includes the required permission. If it doesn't, then return a 403 Forbidden response.
		if !permissions.Include(code) {
			app.notPermittedResponse(w, r)
			return
		}

		// Otherwise they have the required permission so we call the next handler in the chain.
		next.ServeHTTP(w, r)
	}

	// Wrap this with the requireActivatedUser() middleware before returning it.
	return app.requireActivatedUser(fn)
}

func (app *application) enableCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Add the "Vary: Origin" header.
		w.Header().Add("Vary", "Origin")

		// Add the "Vary: Access-Control-Request-Method" header.
		w.Header().Add("Vary", "Origin")

		// Get the value of the request's Origin header.
		origin := r.Header.Get("Origin")

		// Only run this if there's an Origin request header present.
		if origin != "" {
			// Loop through the list of trusted origins, checking to see if the request origin exactly matches one
			// of them. If there are no trusted origins, then the loop won't be iterated.
			for i := range app.config.cors.trustedOrigins {
				if origin == app.config.cors.trustedOrigins[i] {
					// If there is a match, them set a "Access-Control-Allow-Origin" response header with the request
					// origin as the value and break out of the loop.
					w.Header().Set("Access-Control-Allow-Origin", origin)

					// Check if the request has the HTTP method OPTIONS and contains the "Access-Control-Request-Method"
					// header. If it does, then we treat it as a preflight request.
					if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
						// Set the necessary preflight response headers, as discussed previously.
						w.Header().Set("Access-Control-Allow-Methods", "OPTIONS, PUT, PATCH, DELETE")
						w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")

						// Write the headers along with a 200 OK status and return from the middleware with no
						// further action.
						w.WriteHeader(http.StatusOK)
						return
					}
					break
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (app *application) metrics(next http.Handler) http.Handler {
	// Initialize the new expvar variables when the middleware chain is first built.
	totalRequestsReceived := expvar.NewInt("total_requests_received")
	totalResponsesSent := expvar.NewInt("total_responses_sent")
	totalProcessingTimeMicroseconds := expvar.NewInt("total_processing_time_us")
	totalResponsesSentByStatus := expvar.NewMap("total_responses_sent_by_status")

	// The following code will be run for every request...
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Increment the requests received count.
		totalRequestsReceived.Add(1)

		// Call the httpsnoop.CaptureMetrics() function, passing in the next handler in the chain along with the
		// existing http.ResponseWriter and http.Request. This return the metrics struct that we saw above.
		metrics := httpsnoop.CaptureMetrics(next, w, r)

		// Increment the response sent count, like before.
		totalResponsesSent.Add(1)

		// Get the request processing time in microseconds from httpsnoop and increment the cumulative time.
		totalProcessingTimeMicroseconds.Add(metrics.Duration.Microseconds())

		// Use the Add() method to increment the count for the given status code by 1. Note that the expvar map
		// is string-keyed, so we need to use the strconv.Itoa() function to convert the status code (which is an
		// integer) to a string.
		totalResponsesSentByStatus.Add(strconv.Itoa(metrics.Code), 1)
	})
}
