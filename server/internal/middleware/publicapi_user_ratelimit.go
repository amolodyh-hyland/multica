package middleware

import (
	"crypto/sha256"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	publicapiv1 "github.com/multica-ai/multica/server/pkg/publicapi/v1"
	"github.com/redis/go-redis/v9"
)

// PublicAPIUserRateLimit applies a per-PAT fixed-window budget separate from
// the stricter Plugin surface. Only a one-way token digest is stored in Redis.
func PublicAPIUserRateLimit(rdb redis.UniversalClient, limit int, window time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if rdb == nil {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			digest := sha256.Sum256([]byte(BearerToken(r)))
			key := fmt.Sprintf("mul:ratelimit:publicapi:user:%x", digest)
			count, err := rateLimitScript.Run(r.Context(), rdb, []string{key}, int(window.Seconds())).Int64()
			if err != nil {
				slog.Warn("user Public API ratelimit: redis error; allowing request", "error", err)
				next.ServeHTTP(w, r)
				return
			}
			if count > int64(limit) {
				w.Header().Set("Retry-After", fmt.Sprintf("%d", int(window.Seconds())))
				publicapiv1.WriteProblem(w, r, http.StatusTooManyRequests, "rate_limited", "user Public API rate limit exceeded")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
