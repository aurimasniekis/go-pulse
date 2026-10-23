// Package logging configures slog JSON logging (PL-OBS-001).
package logging

import (
	"io"
	"log/slog"
	"strings"
)

// New returns a JSON logger at the given level (debug, info, warn, error).
func New(w io.Writer, level string) *slog.Logger {
	var l slog.Level
	switch strings.ToLower(level) {
	case "debug":
		l = slog.LevelDebug
	case "warn", "warning":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: l}))
}

// Redact shortens a secret-ish identifier for logs, keeping a short prefix.
func Redact(s string) string {
	if len(s) <= 8 {
		return "…"
	}
	return s[:6] + "…"
}
