package limiter

import (
	"math"
	"testing"
	"time"
)

var free = Rule{Name: "free", Algorithm: TokenBucket, Rate: 10, Burst: 10}

func TestTakeFromFullBucket(t *testing.T) {
	tokens, d := take(10, 0, free)
	if !d.Allowed || d.Remaining != 9 || tokens != 9 {
		t.Fatalf("got allowed=%v remaining=%d tokens=%v", d.Allowed, d.Remaining, tokens)
	}
	if d.Quota != 10 || d.Window != time.Second {
		t.Fatalf("policy: q=%d w=%v", d.Quota, d.Window)
	}
	// one token short of full -> next token in 1/rate = 100ms
	if d.Wait != 100*time.Millisecond {
		t.Fatalf("wait %v", d.Wait)
	}
}

func TestEmptyBucketRejects(t *testing.T) {
	tokens, d := take(0, 0, free)
	if d.Allowed {
		t.Fatal("empty bucket allowed a request")
	}
	if tokens != 0 || d.Remaining != 0 {
		t.Fatalf("tokens=%v remaining=%d", tokens, d.Remaining)
	}
	if d.Wait != 100*time.Millisecond {
		t.Fatalf("wait %v, want 100ms", d.Wait)
	}
}

func TestFractionalRefillIsKept(t *testing.T) {
	// 50ms at 10/s is half a token. Rounding that away would starve
	// a client that calls every 50ms.
	tokens, d := take(0, 50*time.Millisecond, free)
	if d.Allowed {
		t.Fatal("half a token should not be enough")
	}
	if tokens < 0.49 || tokens > 0.51 {
		t.Fatalf("tokens %v, want 0.5", tokens)
	}
	if d.Wait != 50*time.Millisecond {
		t.Fatalf("wait %v, want 50ms", d.Wait)
	}
	_, d = take(tokens, 50*time.Millisecond, free)
	if !d.Allowed {
		t.Fatal("two half tokens should make a whole one")
	}
}

func TestRefillNeverExceedsBurst(t *testing.T) {
	tokens, d := take(3, time.Hour, free)
	if !d.Allowed || tokens != 9 {
		t.Fatalf("tokens %v after a long idle, want burst-1 = 9", tokens)
	}
}

func TestLoweredBurstClampsOldBucket(t *testing.T) {
	lowered := free
	lowered.Burst = 2
	tokens, d := take(10, 0, lowered)
	if !d.Allowed || tokens != 1 || d.Remaining != 1 {
		t.Fatalf("tokens=%v remaining=%d, want 1", tokens, d.Remaining)
	}
}

func TestClockGoingBackwards(t *testing.T) {
	tokens, d := take(0, -5*time.Second, free)
	if d.Allowed || tokens != 0 {
		t.Fatalf("negative elapsed refilled the bucket: tokens=%v", tokens)
	}
}

func TestFullBucketHasNoWait(t *testing.T) {
	// After a take the bucket is at most burst-1, so a real decision never
	// has Wait 0. Check the helper directly for the full case.
	if w := untilNextToken(10, free); w != 0 {
		t.Fatalf("full bucket wait %v", w)
	}
}

func TestSlowRule(t *testing.T) {
	slow := Rule{Name: "slow", Algorithm: TokenBucket, Rate: 0.5, Burst: 1}
	_, d := take(0, 0, slow)
	if d.Wait != 2*time.Second || d.Window != 2*time.Second {
		t.Fatalf("wait=%v window=%v, want 2s", d.Wait, d.Window)
	}
}

func TestValidate(t *testing.T) {
	if err := free.Validate(); err != nil {
		t.Fatalf("free rule rejected: %v", err)
	}
	bad := map[string]Rule{
		"empty name":    {Name: "", Algorithm: TokenBucket, Rate: 1, Burst: 1},
		"quote in name": {Name: `fr"ee`, Algorithm: TokenBucket, Rate: 1, Burst: 1},
		"uppercase":     {Name: "Free", Algorithm: TokenBucket, Rate: 1, Burst: 1},
		"algorithm":     {Name: "x", Algorithm: "leaky", Rate: 1, Burst: 1},
		"zero rate":     {Name: "x", Algorithm: TokenBucket, Rate: 0, Burst: 1},
		"tiny rate":     {Name: "x", Algorithm: TokenBucket, Rate: 1e-9, Burst: 10},
		"NaN rate":      {Name: "x", Algorithm: TokenBucket, Rate: math.NaN(), Burst: 1},
		"Inf rate":      {Name: "x", Algorithm: TokenBucket, Rate: math.Inf(1), Burst: 1},
		"zero burst":    {Name: "x", Algorithm: TokenBucket, Rate: 1, Burst: 0},
		"huge burst":    {Name: "x", Algorithm: TokenBucket, Rate: 1, Burst: 2_000_000},
		"day to refill": {Name: "x", Algorithm: TokenBucket, Rate: 0.01, Burst: 1000},
	}
	for name, r := range bad {
		if err := r.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestSlowestAllowedRuleStaysSane(t *testing.T) {
	r := Rule{Name: "x", Algorithm: TokenBucket, Rate: MinRate, Burst: 86}
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	_, d := take(0, 0, r)
	if d.Window != 86000*time.Second || d.Wait != 1000*time.Second {
		t.Fatalf("window=%v wait=%v", d.Window, d.Wait)
	}
}
