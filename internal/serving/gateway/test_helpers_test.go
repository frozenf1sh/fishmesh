package gateway

import (
	"io"
	"log/slog"
	"time"
)

const (
	defaultRequestTimeout  = 5 * time.Second
	defaultShutdownTimeout = 5 * time.Second
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
