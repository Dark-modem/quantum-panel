// ratelimit.go — per-IP token-bucket limiter for the panel's
// UNAUTHENTICATED endpoints (SECURITY-AUDIT-PANEL M1). These endpoints
// are reachable by anyone who can reach the listener:
//
//	GET  /api/agent/artifact/{name}          (release binaries)
//	GET  /api/agent/engine-artifacts[/file]  (R5 manifest + bytes)
//	POST /api/agents/enroll                  (token → identity trade)
//	GET  /api/health
//
// A stolen/lucky token is still the only way through enroll, and
// artifact integrity is enforced client-side by signatures — the limiter
// exists to stop bandwidth exhaustion and brute-force noise, not to be
// the only gate. Authenticated endpoints do not need it (they gate on
// the session/agent credential first and have their own lockouts).

package panel

import (
	"net/http"
	"strconv"
	"sync"
	"time"
)

const (
	// unauthRateLimit: sustained rate per IP per minute.
	unauthRateLimit = 60
	// unauthRateBurst: short-burst allowance (a browser loading the UI
	// + an agent bootstrapping artifact+manifest+enroll at once).
	unauthRateBurst = 120
	// unauthBucketTTL: idle buckets are dropped after this (memory cap).
	unauthBucketTTL = 10 * time.Minute
)

type unauthBucket struct {
	tokens float64
	last   time.Time
}

var (
	unauthLimiterMu sync.Mutex
	unauthLimiter   = map[string]*unauthBucket{}
	unauthLastSweep = time.Now()
)

// unauthAllow consumes one token for ip; reports whether the request
// may proceed. Refill runs lazily at rate unauthRateLimit/minute with a
// burst ceiling; a periodic sweep drops stale buckets.
func unauthAllow(ip string, now time.Time) bool {
	unauthLimiterMu.Lock()
	defer unauthLimiterMu.Unlock()
	if now.Sub(unauthLastSweep) > unauthBucketTTL {
		for k, b := range unauthLimiter {
			if now.Sub(b.last) > unauthBucketTTL {
				delete(unauthLimiter, k)
			}
		}
		unauthLastSweep = now
	}
	b, ok := unauthLimiter[ip]
	if !ok {
		unauthLimiter[ip] = &unauthBucket{tokens: unauthRateBurst - 1, last: now}
		return true
	}
	b.tokens += now.Sub(b.last).Minutes() * unauthRateLimit
	if b.tokens > unauthRateBurst {
		b.tokens = unauthRateBurst
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// limitUnauth is the middleware wired around the unauthenticated routes.
// Rejections are generic 429s with Retry-After; no rate-limit state is
// reflected back (bucket counts are not a side channel worth exporting).
func limitUnauth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !unauthAllow(ClientIP(r), time.Now()) {
			w.Header().Set("Retry-After", strconv.Itoa(10))
			http.Error(w, "too many requests", http.StatusTooManyRequests)
			return
		}
		next(w, r)
	}
}

// unauthLimiterResetForTest empties the buckets between tests.
func unauthLimiterResetForTest() {
	unauthLimiterMu.Lock()
	defer unauthLimiterMu.Unlock()
	unauthLimiter = map[string]*unauthBucket{}
	unauthLastSweep = time.Now()
}
