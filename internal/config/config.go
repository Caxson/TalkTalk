// Package config loads and validates TalkTalk's runtime configuration from
// environment variables. Invalid values fail fast with every problem reported
// at once, so a misconfigured deployment never starts half-working.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"time"
)

// Environment variable names.
const (
	EnvHTTPAddr        = "TALKTALK_HTTP_ADDR"
	EnvLogLevel        = "TALKTALK_LOG_LEVEL"
	EnvLogFormat       = "TALKTALK_LOG_FORMAT"
	EnvShutdownTimeout = "TALKTALK_SHUTDOWN_TIMEOUT"
)

// Defaults applied when a variable is unset or empty.
const (
	DefaultHTTPAddr        = ":8080"
	DefaultShutdownTimeout = 10 * time.Second
)

// LogFormat selects the slog handler.
type LogFormat string

const (
	LogFormatText LogFormat = "text"
	LogFormatJSON LogFormat = "json"
)

// Config is the validated, immutable runtime configuration.
type Config struct {
	HTTPAddr        string
	LogLevel        slog.Level
	LogFormat       LogFormat
	ShutdownTimeout time.Duration
}

// LookupFunc matches os.LookupEnv so tests can inject an environment.
type LookupFunc func(key string) (string, bool)

// Load reads configuration through lookup and validates it.
func Load(lookup LookupFunc) (Config, error) {
	get := func(key, fallback string) string {
		if v, ok := lookup(key); ok {
			if v = strings.TrimSpace(v); v != "" {
				return v
			}
		}
		return fallback
	}

	var errs []error
	cfg := Config{}

	addr, err := parseAddr(get(EnvHTTPAddr, DefaultHTTPAddr))
	errs = appendErr(errs, EnvHTTPAddr, err)
	cfg.HTTPAddr = addr

	level, err := parseLevel(get(EnvLogLevel, "info"))
	errs = appendErr(errs, EnvLogLevel, err)
	cfg.LogLevel = level

	format, err := parseFormat(get(EnvLogFormat, string(LogFormatText)))
	errs = appendErr(errs, EnvLogFormat, err)
	cfg.LogFormat = format

	timeout, err := parseTimeout(get(EnvShutdownTimeout, DefaultShutdownTimeout.String()))
	errs = appendErr(errs, EnvShutdownTimeout, err)
	cfg.ShutdownTimeout = timeout

	if err := errors.Join(errs...); err != nil {
		return Config{}, fmt.Errorf("invalid configuration: %w", err)
	}
	return cfg, nil
}

func appendErr(errs []error, key string, err error) []error {
	if err == nil {
		return errs
	}
	return append(errs, fmt.Errorf("%s: %w", key, err))
}

func parseAddr(v string) (string, error) {
	_, port, err := net.SplitHostPort(v)
	if err != nil {
		return "", fmt.Errorf("want host:port, got %q", v)
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 0 || n > 65535 {
		return "", fmt.Errorf("port %q out of range 0-65535", port)
	}
	return v, nil
}

func parseLevel(v string) (slog.Level, error) {
	var level slog.Level
	if err := level.UnmarshalText([]byte(v)); err != nil {
		return 0, fmt.Errorf("want debug|info|warn|error, got %q", v)
	}
	return level, nil
}

func parseFormat(v string) (LogFormat, error) {
	switch f := LogFormat(strings.ToLower(v)); f {
	case LogFormatText, LogFormatJSON:
		return f, nil
	default:
		return "", fmt.Errorf("want text|json, got %q", v)
	}
}

func parseTimeout(v string) (time.Duration, error) {
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("want a Go duration like 10s, got %q", v)
	}
	if d <= 0 {
		return 0, fmt.Errorf("must be positive, got %s", d)
	}
	return d, nil
}
