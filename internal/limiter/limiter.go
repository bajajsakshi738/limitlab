// Package limiter decides whether a request may go through.
package limiter

import (
	"context"
	"fmt"
	"math"
	"time"
)

type Algorithm string

const TokenBucket Algorithm = "token_bucket"

// Rule is one rate limit. Rate is a float because the fallback limiter
// (later) splits a rule across instances and 10/3 isn't a whole number.
type Rule struct {
	Name      string
	Algorithm Algorithm
	Rate      float64 // requests per second
	Burst     int
}

// Bounds keep the duration math far away from overflow. Nobody needs a
// limit slower than one request per 1000 seconds here.
const (
	MinRate  = 0.001
	MaxRate  = 1_000_000
	MaxBurst = 1_000_000
)

// Validate is called when rules are loaded, so a bad rule stops startup
// instead of failing on every request.
func (r Rule) Validate() error {
	if r.Name == "" || len(r.Name) > 32 {
		return fmt.Errorf("rule name %q: must be 1-32 characters", r.Name)
	}
	for _, c := range r.Name {
		// the name ends up inside a quoted header value
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return fmt.Errorf("rule name %q: only a-z, 0-9, - and _ allowed", r.Name)
		}
	}
	if r.Algorithm != TokenBucket {
		return fmt.Errorf("rule %q: unsupported algorithm %q", r.Name, r.Algorithm)
	}
	if math.IsNaN(r.Rate) || r.Rate < MinRate || r.Rate > MaxRate {
		return fmt.Errorf("rule %q: rate %v out of range", r.Name, r.Rate)
	}
	if r.Burst < 1 || r.Burst > MaxBurst {
		return fmt.Errorf("rule %q: burst %d out of range", r.Name, r.Burst)
	}
	if float64(r.Burst)/r.Rate > 86400 {
		return fmt.Errorf("rule %q: bucket would take over a day to refill", r.Name)
	}
	return nil
}

// Decision is everything the HTTP layer needs to answer and set headers.
//
// How the token bucket maps onto the RateLimit headers:
//   - Quota (q) is the burst, Window (w) is how long an empty bucket takes
//     to fill. So a quiet client can send q at once, then q per w after that.
//   - Wait (t) is the time until the bucket holds one more whole token,
//     0 when it's already full. Headers round it up to whole seconds.
type Decision struct {
	Allowed   bool
	Remaining int           // whole requests left right now
	Wait      time.Duration // until more quota is available; 0 when full
	Quota     int
	Window    time.Duration
}

type Limiter interface {
	// Allow spends one unit of quota for key under rule r, if there is any.
	Allow(ctx context.Context, key string, r Rule) (Decision, error)
}
