package limiter

import (
	"math"
	"time"
)

// Float math can land on 0.9999999999 when it "should" be 1. Without this
// a client would occasionally get a 429 it didn't deserve.
const epsilon = 1e-9

// take refills the bucket for the time that passed and tries to spend one
// token. It's kept free of locks and clocks so it can be tested on its own,
// and so the Redis Lua script later can follow the same steps.
func take(tokens float64, elapsed time.Duration, r Rule) (float64, Decision) {
	if elapsed < 0 {
		// clock went backwards; refill nothing rather than go negative
		elapsed = 0
	}
	burst := float64(r.Burst)

	// The min() also handles a rule being lowered: an old bucket holding
	// 100 tokens gets cut to the new burst on its next request.
	tokens = math.Min(burst, tokens+elapsed.Seconds()*r.Rate)

	d := Decision{Quota: r.Burst, Window: fullRefill(r)}
	if tokens+epsilon >= 1 {
		tokens = math.Max(0, tokens-1)
		d.Allowed = true
	}
	d.Remaining = int(math.Floor(tokens + epsilon))
	d.Wait = untilNextToken(tokens, r)
	return tokens, d
}

// untilNextToken is how long until the bucket holds one more whole token.
func untilNextToken(tokens float64, r Rule) time.Duration {
	if tokens+epsilon >= float64(r.Burst) {
		return 0
	}
	next := math.Floor(tokens+epsilon) + 1
	return ceilMillis((next - tokens) / r.Rate)
}

// fullRefill is how long an empty bucket takes to fill up.
func fullRefill(r Rule) time.Duration {
	return ceilMillis(float64(r.Burst) / r.Rate)
}

func ceilMillis(seconds float64) time.Duration {
	ms := math.Ceil(seconds*1000 - epsilon)
	// Validate keeps rules well inside this, but a float that doesn't fit
	// in an int64 converts to garbage, so cap it anyway (about 11 days).
	return time.Duration(math.Min(ms, 1e9)) * time.Millisecond
}
