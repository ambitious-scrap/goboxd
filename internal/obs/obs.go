package obs

import (
	"context"
	"log/slog"
	"os"
	"sync/atomic"
	"time"
)

var (
	TotalRequests atomic.Int64
	InFlight      atomic.Int64
	TotalErrors   atomic.Int64

	// lastInternalErrorUnixNano holds the time of the most recent internal
	// (server-side) error, in Unix nanoseconds. Zero means none has occurred.
	lastInternalErrorUnixNano atomic.Int64
)

// MarkInternalError records that an internal error just occurred.
func MarkInternalError() {
	lastInternalErrorUnixNano.Store(time.Now().UnixNano())
}

// LastInternalError returns the time of the most recent internal error and
// whether one has occurred.
func LastInternalError() (time.Time, bool) {
	ns := lastInternalErrorUnixNano.Load()
	if ns == 0 {
		return time.Time{}, false
	}
	return time.Unix(0, ns), true
}

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
