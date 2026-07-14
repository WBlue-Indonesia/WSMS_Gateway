package api

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
)

// rateLimiter holds a per-client token bucket. Protects the fleet (and the SIMs'
// ban-risk posture) from a single client flooding submits.
type rateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*clientBucket
	perSec  float64
	burst   int
}

type clientBucket struct {
	lim  *rate.Limiter
	seen time.Time
}

func newRateLimiter(perSec float64, burst int) *rateLimiter {
	return &rateLimiter{buckets: map[string]*clientBucket{}, perSec: perSec, burst: burst}
}

func (rl *rateLimiter) allow(clientID string) bool {
	rl.mu.Lock()
	b, ok := rl.buckets[clientID]
	if !ok {
		b = &clientBucket{lim: rate.NewLimiter(rate.Limit(rl.perSec), rl.burst)}
		rl.buckets[clientID] = b
	}
	b.seen = time.Now()
	rl.mu.Unlock()
	return b.lim.Allow()
}

// middleware enforces the limit for the authenticated client (client_id set by clientAuth).
func (rl *rateLimiter) middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetString("client_id")
		if id != "" && !rl.allow(id) {
			c.Header("Retry-After", "1")
			abort(c, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}
		c.Next()
	}
}
