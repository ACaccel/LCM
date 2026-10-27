package trigger_test

import "log/slog"

func nopLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }
