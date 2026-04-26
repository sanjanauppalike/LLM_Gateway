package ratelimit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	DefaultMaxRequestsPerWindow int64 = 5
	DefaultWindow                     = time.Minute
)

var tokenBucketLua = redis.NewScript(`
local key = KEYS[1]
local capacity = tonumber(ARGV[1])
local window_ms = tonumber(ARGV[2])
local now_ms = tonumber(ARGV[3])

local fill_rate = capacity / window_ms
local bucket = redis.call('HMGET', key, 'tokens', 'last_update')
local tokens = tonumber(bucket[1])
local last_update = tonumber(bucket[2])

if not tokens then
	tokens = capacity
	last_update = now_ms
else
	local elapsed_ms = math.max(0, now_ms - last_update)
	local generated = elapsed_ms * fill_rate
	tokens = math.min(capacity, tokens + generated)
end

local allowed = 0
if tokens >= 1 then
	tokens = tokens - 1
	allowed = 1
	redis.call('HMSET', key, 'tokens', tokens, 'last_update', now_ms)
	redis.call('PEXPIRE', key, window_ms)
end

local remaining = math.floor(tokens)
local reset_ms = now_ms
if remaining < capacity then
	local missing = capacity - remaining
	reset_ms = now_ms + math.ceil(missing / fill_rate)
end

return {allowed, remaining, reset_ms}
`)

type Limiter struct {
	client      *redis.Client
	maxRequests int64
	window      time.Duration
	failOpen    bool
	now         func() time.Time
	evaluate    func(context.Context, string, int64, time.Duration, int64) (int64, int64, int64, error)
}

type Options struct {
	MaxRequests int64
	Window      time.Duration
	FailOpen    bool
}

func NewLimiterWithOptions(host string, port string, opts Options) (*Limiter, error) {
	if opts.MaxRequests <= 0 {
		opts.MaxRequests = DefaultMaxRequestsPerWindow
	}
	if opts.Window <= 0 {
		opts.Window = DefaultWindow
	}

	client := redis.NewClient(&redis.Options{
		Addr: fmt.Sprintf("%s:%s", host, port),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, err
	}

	rl := &Limiter{
		client:      client,
		maxRequests: opts.MaxRequests,
		window:      opts.Window,
		failOpen:    opts.FailOpen,
		now:         time.Now,
	}
	rl.evaluate = func(ctx context.Context, key string, capacity int64, window time.Duration, nowMs int64) (int64, int64, int64, error) {
		res, err := tokenBucketLua.Run(ctx, rl.client, []string{key}, capacity, int64(window/time.Millisecond), nowMs).Result()
		if err != nil {
			return 0, 0, 0, err
		}
		vals := res.([]interface{})
		return vals[0].(int64), vals[1].(int64), vals[2].(int64), nil
	}

	return rl, nil
}

func (rl *Limiter) Close() error {
	if rl == nil || rl.client == nil {
		return nil
	}
	return rl.client.Close()
}

func (rl *Limiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" || !strings.HasPrefix(authHeader, "Bearer ") {
			http.Error(w, "missing or invalid Authorization header", http.StatusUnauthorized)
			return
		}

		tokenHash := hashToken(strings.TrimPrefix(authHeader, "Bearer "))
		now := rl.now().UTC()
		allowed, remaining, resetMs, err := rl.evaluate(
			r.Context(),
			fmt.Sprintf("ratelimit:tb:%s", tokenHash),
			rl.maxRequests,
			rl.window,
			now.UnixMilli(),
		)
		if err != nil {
			if rl.failOpen {
				log.Printf("rate limiter backend unavailable; failing open: %v", err)
				next.ServeHTTP(w, r)
				return
			}
			http.Error(w, "rate limiter unavailable", http.StatusServiceUnavailable)
			return
		}

		resetTime := time.UnixMilli(resetMs)
		retryAfter := int(resetTime.Sub(now).Seconds())
		if retryAfter < 1 {
			retryAfter = 1
		}

		w.Header().Set("X-RateLimit-Limit", strconv.FormatInt(rl.maxRequests, 10))
		w.Header().Set("X-RateLimit-Remaining", strconv.FormatInt(remaining, 10))
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(resetTime.Unix(), 10))

		if allowed == 0 {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":"rate limit exceeded"}`))
			return
		}

		next.ServeHTTP(w, r)
	})
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:16])
}
