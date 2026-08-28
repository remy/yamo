package ui

import (
	"context"
	"fmt"
	"time"
)

func sprintf(format string, args ...any) string {
	if len(args) == 0 {
		return format
	}
	return fmt.Sprintf(format, args...)
}

// contextWithTimeout is a small wrapper so callers in this package do not each
// import context for a one-line deadline.
func contextWithTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}
