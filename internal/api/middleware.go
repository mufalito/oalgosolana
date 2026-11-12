package api

import (
	"crypto/subtle"
	"net/http"
	"strings"
	"sync"

	"github.com/mufalito/oalgosolana/internal/apierr"
	"golang.org/x/time/rate"
)

// KeyAuth rejects requests without a valid Bearer token. Comparison is
// constant-time per configured key.
func KeyAuth(keys []string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !ok || token == "" {
				apierr.WriteError(w, apierr.Errorf(http.StatusUnauthorized, apierr.CodeUnauthorized, "missing bearer token"))
				return
			}
			for _, k := range keys {
				if subtle.ConstantTimeCompare([]byte(k), []byte(token)) == 1 {
					next.ServeHTTP(w, r)
					return
				}
			}
			apierr.WriteError(w, apierr.Errorf(http.StatusUnauthorized, apierr.CodeUnauthorized, "invalid API key"))
		})
	}
}

// rateLimiters is a per-key token-bucket limiter set.
type rateLimiters struct {
	mu        sync.Mutex
	rps       rate.Limit
	burst     int
	limitters map[string]*rate.Limiter
}

func newRateLimiters(rps float64, burst int) *rateLimiters {
	return &rateLimiters{rps: rate.Limit(rps), burst: burst, limitters: make(map[string]*rate.Limiter)}
}

func (rl *rateLimiters) get(key string) *rate.Limiter {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	l, ok := rl.limitters[key]
	if !ok {
		l = rate.NewLimiter(rl.rps, rl.burst)
		rl.limitters[key] = l
	}
	return l
}

// RateLimit throttles per API key (identified by the Bearer token).
func RateLimit(rps float64, burst int) func(http.Handler) http.Handler {
	rl := newRateLimiters(rps, burst)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if key != "" && !rl.get(key).Allow() {
				w.Header().Set("Retry-After", "1")
				apierr.WriteError(w, apierr.Errorf(http.StatusTooManyRequests, apierr.CodeRateLimited, "rate limit exceeded"))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
