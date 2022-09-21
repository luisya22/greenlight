package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func (app *application) serve() error {
	// Declare an HTTP server using the same settings as in our main() function.
	srv := &http.Server{
		Addr:         fmt.Sprintf(":%d", app.config.port),
		Handler:      app.routes(),
		IdleTimeout:  time.Minute,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	// Create a shutdownError channel. We will use this to receive any errors returned by the graceful Shutdown()
	// function.
	shutdownError := make(chan error)

	// Start a background goroutine
	go func() {
		// Create a quit channel which carries os.Signal values.
		quit := make(chan os.Signal, 1)

		// Use signal.Notify() to listen for incoming SIGINT and SIGTERM signals and relay them to the quit channel.
		// Any other signals will no tbe caught by signal.Notify() and will retain their default behavior.
		signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

		// Read the signal from the quit channel. This code will block until a signal is received.
		s := <-quit

		// Log a message to say that the signal has been caught. Notice that we also call the String() method on
		// the signal to get the signal name and include it in the log entry properties.
		app.logger.PrintInfo("shutting down server", map[string]string{"signal": s.String()})

		// Create a context with a 20-seconds timeout.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		// Call Shutdown() on our server, passing in the context we just made. Shutdown() will return nil if
		// the graceful shutdown was successful, or an error (which may happen because of a problem closing the
		// listeners, or because the shutdown didn't complete before the 20-seconds context deadline is hit).
		// We relay this return value to the shutdownError channel.
		err := srv.Shutdown(ctx)
		if err != nil {
			shutdownError <- err
		}

		// Log a message to say that we're waiting for any background goroutines to complete their tasks.
		app.logger.PrintInfo("completing background tasks", map[string]string{
			"addr": srv.Addr,
		})

		// Call Wait() to block until our WaitGroup counter is zero --- essentially blocking until the background
		// goroutines have finished. Then we return nil on the shutdown completed without any issues.
		app.wg.Wait()
		shutdownError <- nil

	}()

	// Likewise log a "starting server" message.
	app.logger.PrintInfo("starting server", map[string]string{
		"addr": srv.Addr,
		"env":  app.config.env,
	})

	// calling Shutdown() ou our server will cause ListenAndServe() to immediately return a http.ErrServerClosed error.
	// So if we see this error, it is actually a good thing and an indication that the graceful shutdown has started.
	// So we check specifically for this, only returning the error if it is NOT http.ErrServerClosed.
	err := srv.ListenAndServe()
	if !errors.Is(err, http.ErrServerClosed) {
		return err
	}

	// Otherwise, we wait to receive the return value from Shutdown() on the shutdownError channel. If return value
	// is an error, we know that there was a problem with graceful shutdown and we return the error.
	err = <-shutdownError
	if err != nil {
		return err
	}

	// At this point we know that the graceful shutdown completed successfully and we log a "stopped server" message.
	app.logger.PrintInfo("stopped server", map[string]string{
		"addr": srv.Addr,
	})

	return nil
}
