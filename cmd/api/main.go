package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"greenlight.luismatosgarcia.dev/internal/data"
	"greenlight.luismatosgarcia.dev/internal/jsonlog"
	"log"
	"net/http"
	"os"
	"time"

	_ "github.com/lib/pq"
)

// Declare a string containing the application version number.
const version = "1.0.0"

// Define a config struct to hold all the configuration settings for our application. For now, the only
// configuration settings will be the network port that we want the server to listen on, and the name of the current
// operating environment for the application (development, staging, production, etc.). We will read in these
// configuration settings from command-line flags when the application starts.
type config struct {
	port int
	env  string
	db   struct {
		dsn          string
		maxOpenConns int
		maxIdleConns int
		maxIdleTime  string
	}
}

// Define an application struct to hold the dependencies for HTTP handlers, helpers, and middleware. At the moment
// this only contains a copy of the config struct and a logger this only contains a copy of the config struct
// and a logger, but it will grow to include a lot more as our build progresses.
type application struct {
	config config
	logger *jsonlog.Logger
	models data.Models
}

func main() {
	// Declare an instance of the config struct.
	var cfg config

	// Read the value of the port and env command-line flags into the config struct. We default to using the port
	// default to using the port number 4000 and the environment "development"if no corresponding flags are provided.
	flag.IntVar(&cfg.port, "port", 4000, "API server port")
	flag.StringVar(&cfg.env, "env", "development", "Environment (development|staging|production)")

	// Read the DSN value from the db-dsn command-line flag into the config struct. We default to using our
	// development DSN if no flag is provided.
	flag.StringVar(&cfg.db.dsn, "db-dsn", os.Getenv("GREENLIGHT_DB_DSN"), "PostgreSQL DSN")

	// Read the connection pool settings from command-line flags into the config struct.
	// Notice the default values that we're using?
	flag.IntVar(&cfg.db.maxOpenConns, "db-max-open-conns", 25, "PostgreSQL max open connections")
	flag.IntVar(&cfg.db.maxIdleConns, "db-max-idle-conns", 25, "PostgreSQL max idle connections")
	flag.StringVar(&cfg.db.maxIdleTime, "db-max-idle-time", "15m", "PostgreSQL max connection idle time")

	flag.Parse()

	// Initialize a new jsonlog.Logger which writes any messages *at or above* the INFO severity level to the
	// standard out stream.
	logger := jsonlog.New(os.Stdout, jsonlog.LevelInfo)

	// Call the openDB() helper function to create the connection pool, passing in the config struct.
	// If this return an error, we log it and exit the application immediately.
	db, err := openDB(cfg)
	if err != nil {
		// Use the PrintFatal() method to write a log entry containing the error at the FATAL level and exit.
		// We have no additional properties to include in the log entry, so we pass nil as the second parameter.
		logger.PrintFatal(err, nil)
	}

	// Defer a call to db.Close() so that the connection pool is closed before the main() function exits.
	defer db.Close()

	//Also log a message to say that the connection pool has been successfully established.
	logger.PrintInfo("database connection pool established", nil)

	// Declare an instance of the application struct, containing the config struct and the logger.
	app := &application{
		config: cfg,
		logger: logger,
		models: data.NewModels(db),
	}

	// Declare an HTTP server with some sensible timeout settings, which listens on the port provided
	// in the config struct and uses the servermux we created above as the handler.
	srv := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.port),
		Handler:      app.routes(),
		ErrorLog:     log.New(logger, "", 0),
		IdleTimeout:  time.Minute,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	// Start the HTTP server.
	logger.PrintInfo("starting server", map[string]string{
		"addr": srv.Addr,
		"env":  cfg.env,
	})
	err = srv.ListenAndServe()
	logger.PrintFatal(err, nil)
}

// The openDB() function returns a sql.DB connection pool.
func openDB(cfg config) (*sql.DB, error) {
	// Use sql.Open() to create an empty connection pool, using the DSN from the config struct.
	db, err := sql.Open("postgres", cfg.db.dsn)
	if err != nil {
		return nil, err
	}

	// Set the maximum number of open (in-use + idle) connections in the pool. Note that passing a value less than or
	// equal to 0 wil mean there is no limit.
	db.SetMaxOpenConns(cfg.db.maxOpenConns)

	// Set the maximum number of idle connections in the pool.
	db.SetMaxIdleConns(cfg.db.maxIdleConns)

	// Use the time.ParseDuration(0 function to convert the idle timeout duration string to a time.Duration type.
	duration, err := time.ParseDuration(cfg.db.maxIdleTime)
	if err != nil {
		return nil, err
	}

	// Set the maximum idle timeout.
	db.SetConnMaxIdleTime(duration)

	// Create a context with a 5-second timeout deadline.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Use PingContext() to establish a new connection to the database, passing in the context we created above as a
	// parameter. If the connection couldn't be established successfully within the 5-second deadline, then this
	// will return an error.
	err = db.PingContext(ctx)
	if err != nil {
		return nil, err
	}

	// Return the sql.Db connection pool.
	return db, nil
}
