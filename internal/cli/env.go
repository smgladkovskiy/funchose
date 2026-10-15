package cli

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"
)

// envReader reads flag defaults from the environment and keeps every value it
// could not parse: a typo in a CI variable must stop funchose, not quietly turn
// into the default.
type envReader struct{ errs []error }

func (e *envReader) text(key, def string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}

	return def
}

func (e *envReader) number(key string, def int64) int64 {
	value := os.Getenv(key)
	if value == "" {
		return def
	}

	number, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		e.errs = append(e.errs, fmt.Errorf("%w: %s=%q is not a whole number", errEnv, key, value))

		return def
	}

	return number
}

func (e *envReader) boolean(key string) bool {
	value := os.Getenv(key)
	if value == "" {
		return false
	}

	flag, err := strconv.ParseBool(value)
	if err != nil {
		e.errs = append(e.errs, fmt.Errorf("%w: %s=%q is not true or false", errEnv, key, value))
	}

	return flag
}

func (e *envReader) duration(key string, def time.Duration) time.Duration {
	value := os.Getenv(key)
	if value == "" {
		return def
	}

	duration, err := time.ParseDuration(value)
	if err != nil {
		e.errs = append(e.errs, fmt.Errorf("%w: %s=%q is not a duration such as 5m", errEnv, key, value))

		return def
	}

	return duration
}

func (e *envReader) err() error { return errors.Join(e.errs...) }
