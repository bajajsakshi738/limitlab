// Package plans maps plan names to their rate limit rule.
package plans

import "limitlab/internal/limiter"

// Defaults are hardcoded for now; they move to Postgres later.
// Burst is one second's worth of rate, so a client that was quiet can't
// fire more than a second's worth of requests at once.
func Defaults() map[string]limiter.Rule {
	return map[string]limiter.Rule{
		"free": {Name: "free", Algorithm: limiter.TokenBucket, Rate: 10, Burst: 10},
		"pro":  {Name: "pro", Algorithm: limiter.TokenBucket, Rate: 100, Burst: 100},
	}
}
