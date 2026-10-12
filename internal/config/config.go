// Package config loads and validates TalkTalk's runtime configuration from
// environment variables. Invalid values fail fast with every problem reported
// at once, so a misconfigured deployment never starts half-working.
package config

import (
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

	LLM          LLM
	Speech       Speech
	Conversation Conversation
}

// LookupFunc matches os.LookupEnv so tests can inject an environment.
type LookupFunc func(key string) (string, bool)

// Load reads configuration through lookup and validates it.
func Load(lookup LookupFunc) (Config, error) {
	r := &reader{lookup: lookup}
	cfg := Config{}

	addr, err := parseAddr(r.str(EnvHTTPAddr, DefaultHTTPAddr))
	r.errs = appendErr(r.errs, EnvHTTPAddr, err)
	cfg.HTTPAddr = addr

	level, err := parseLevel(r.str(EnvLogLevel, "info"))
	r.errs = appendErr(r.errs, EnvLogLevel, err)
	cfg.LogLevel = level

	format, err := parseFormat(r.str(EnvLogFormat, string(LogFormatText)))
	r.errs = appendErr(r.errs, EnvLogFormat, err)
	cfg.LogFormat = format

	timeout, err := parseTimeout(r.str(EnvShutdownTimeout, DefaultShutdownTimeout.String()))
	r.errs = appendErr(r.errs, EnvShutdownTimeout, err)
	cfg.ShutdownTimeout = timeout

	loadProviders(r, &cfg)

	if err := r.err(); err != nil {
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
