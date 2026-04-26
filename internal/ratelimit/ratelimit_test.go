package ratelimit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestMiddlewareReturns429WhenBucketRejected(t *testing.T) {
	rl := &Limiter{
		maxRequests: 1,
		window:      time.Minute,
		now:         func() time.Time { return time.Unix(100, 0) },
		evaluate: func(context.Context, string, int64, time.Duration, int64) (int64, int64, int64, error) {
			return 0, 0, time.Unix(101, 0).UnixMilli(), nil
		},
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer test")
	rl.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("next handler should not run")
	})).ServeHTTP(rr, req)

	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", rr.Code)
	}
}
