package test_utils

import (
	"os"
	"time"

	"github.com/rs/zerolog"
)

// Logger used for tests with lowest level of logging
func Logger() zerolog.Logger {
	zerolog.SetGlobalLevel(zerolog.TraceLevel)
	return zerolog.New(zerolog.ConsoleWriter{
		NoColor:    true,
		Out:        os.Stdout,
		TimeFormat: time.RFC3339,
	}).With().Timestamp().Logger()
}
