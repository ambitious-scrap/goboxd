package obs

import (
	"context"
	"log/slog"
	"os"
	"sync/atomic"
)

var (
	TotalRequests atomic.Int64
	InFlight      atomic.Int64
	TotalErrors   atomic.Int64
)

var logger = slog.New(slog.NewJSONHandler(os.Stdout, nil))

type key struct{}

// WithRequestID attaches a request id to the context logger.
func WithRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, key{}, requestID)
}

func Log(ctx context.Context, msg string, args ...any) {
	if rid, ok := ctx.Value(key{}).(string); ok {
		args = append([]any{"request_id", rid}, args...)
	}
	logger.InfoContext(ctx, msg, args...)
}

func Error(ctx context.Context, msg string, args ...any) {
	if rid, ok := ctx.Value(key{}).(string); ok {
		args = append([]any{"request_id", rid}, args...)
	}
	logger.ErrorContext(ctx, msg, args...)
}
