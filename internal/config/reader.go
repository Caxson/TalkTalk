package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// reader reads typed values and accumulates every validation error, so one
// startup reports all misconfigured variables at once.
type reader struct {
	lookup LookupFunc
	errs   []error
}

func (r *reader) fail(key string, err error) {
	r.errs = append(r.errs, fmt.Errorf("%s: %w", key, err))
}

func (r *reader) err() error { return errors.Join(r.errs...) }

func (r *reader) str(key, fallback string) string {
	if v, ok := r.lookup(key); ok {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return fallback
}

func (r *reader) required(key string) string {
	v := r.str(key, "")
	if v == "" {
		r.fail(key, errors.New("is required"))
	}
	return v
}

func (r *reader) intIn(key string, fallback, lo, hi int) int {
	raw := r.str(key, "")
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < lo || n > hi {
		r.fail(key, fmt.Errorf("want an integer in %d-%d, got %q", lo, hi, raw))
		return fallback
	}
	return n
}

func (r *reader) floatIn(key string, fallback, lo, hi float64) float64 {
	raw := r.str(key, "")
	if raw == "" {
		return fallback
	}
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil || f < lo || f > hi {
		r.fail(key, fmt.Errorf("want a number in %g-%g, got %q", lo, hi, raw))
		return fallback
	}
	return f
}

func (r *reader) oneOf(key string, fallback int, allowed ...int) int {
	n := r.intIn(key, fallback, 0, 1<<30)
	for _, a := range allowed {
		if n == a {
			return n
		}
	}
	r.fail(key, fmt.Errorf("want one of %v, got %d", allowed, n))
	return fallback
}

func (r *reader) jsonObject(key string) map[string]any {
	raw := r.str(key, "")
	if raw == "" {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		r.fail(key, fmt.Errorf("want a JSON object: %v", err))
		return nil
	}
	return m
}

func (r *reader) list(key string) []string {
	var out []string
	for _, p := range strings.Split(r.str(key, ""), ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (r *reader) boolean(key string, fallback bool) bool {
	raw := r.str(key, "")
	if raw == "" {
		return fallback
	}
	b, err := strconv.ParseBool(raw)
	if err != nil {
		r.fail(key, fmt.Errorf("want true or false, got %q", raw))
		return fallback
	}
	return b
}
