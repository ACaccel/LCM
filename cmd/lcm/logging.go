package main

import (
	"log"
	"log/slog"
)

// slogErrorLog routes net/http's internal error messages into slog.
func slogErrorLog(l *slog.Logger) *log.Logger {
	return slog.NewLogLogger(l.Handler(), slog.LevelError)
}
