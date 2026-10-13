package limiter

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newClock() *fakeClock { return &fakeClock{t: time.Unix(1_700_000_000, 0)} }

func TestMemoryBurstThenReject(t *testing.T) {
	clock := newClock()
	m := NewMemory(clock.Now)
	ctx := context.Background()

	for i := 0; i < 10; i++ {
		d, err := m.Allow(ctx, "key1", free)
		if err != nil || !d.Allowed {
			t.Fatalf("request %d rejected: %v", i, err)
		}
	}
	if d, _ := m.Allow(ctx, "key1", free); d.Allowed {
		t.Fatal("11th request allowed")
	}

	// a different key has its own bucket
	if d, _ := m.Allow(ctx, "key2", free); !d.Allowed {
		t.Fatal("other key was limited")
	}

	clock.Add(100 * time.Millisecond)
	if d, _ := m.Allow(ctx, "key1", free); !d.Allowed {
		t.Fatal("token should have refilled after 100ms")
	}
}

func TestMemorySustainedRate(t *testing.T) {
	// 100 req/s offered for 10s against 10/s burst 10:
	// at most burst + rate*T = 10 + 100 = 110 should get through.
	clock := newClock()
	m := NewMemory(clock.Now)
	allowed := 0
	for i := 0; i < 1000; i++ {
		if d, _ := m.Allow(context.Background(), "k", free); d.Allowed {
			allowed++
		}
		clock.Add(10 * time.Millisecond)
	}
	if allowed < 100 || allowed > 110 {
		t.Fatalf("allowed %d, want 100..110", allowed)
	}
}

func TestMemoryConcurrentNoOverspend(t *testing.T) {
	// clock doesn't move, so exactly burst requests may pass no matter
	// how many goroutines race for them
	clock := newClock()
	m := NewMemory(clock.Now)
	rule := Rule{Name: "r", Algorithm: TokenBucket, Rate: 1, Burst: 50}

	var allowed atomic.Int64
	var wg sync.WaitGroup
	for g := 0; g < 100; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 10; i++ {
				if d, _ := m.Allow(context.Background(), "shared", rule); d.Allowed {
					allowed.Add(1)
				}
			}
		}()
	}
	wg.Wait()
	if allowed.Load() != 50 {
		t.Fatalf("allowed %d, want exactly 50", allowed.Load())
	}
}

func TestMemoryRejectsBadRules(t *testing.T) {
	m := NewMemory(nil)
	bad := []Rule{
		{Name: "x", Algorithm: "leaky", Rate: 1, Burst: 1},
		{Name: "x", Algorithm: TokenBucket, Rate: 0, Burst: 1},
		{Name: "x", Algorithm: TokenBucket, Rate: 1, Burst: 0},
	}
	// Validate has its own detailed test; this just checks Allow calls it.
	for _, r := range bad {
		if _, err := m.Allow(context.Background(), "k", r); err == nil {
			t.Errorf("rule %+v accepted", r)
		}
	}
}

func TestSweepForgetsOnlyFullBuckets(t *testing.T) {
	clock := newClock()
	m := NewMemory(clock.Now)
	ctx := context.Background()

	m.Allow(ctx, "idle", free)
	for i := 0; i < 10; i++ {
		m.Allow(ctx, "busy", free)
	}
	// idle is back to full after 100ms, busy needs 1s
	clock.Add(1500 * time.Millisecond)
	if n := m.Sweep(); n != 1 {
		t.Fatalf("swept %d, want 1", n)
	}
	if m.Len() != 1 {
		t.Fatalf("%d buckets left, want 1", m.Len())
	}
	clock.Add(time.Second)
	m.Sweep()
	if m.Len() != 0 {
		t.Fatal("busy bucket should be gone once it refilled")
	}
}

func TestPlanChangeStartsFreshBucket(t *testing.T) {
	m := NewMemory(newClock().Now)
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		m.Allow(ctx, "k", free)
	}
	pro := Rule{Name: "pro", Algorithm: TokenBucket, Rate: 100, Burst: 100}
	if d, _ := m.Allow(ctx, "k", pro); !d.Allowed || d.Remaining != 99 {
		t.Fatalf("pro bucket not fresh: %+v", d)
	}
}

func TestOutOfOrderClockDoesNotRefillTwice(t *testing.T) {
	// Two requests can reach the bucket with their times out of order.
	// The later time must win, or the gap gets refilled twice.
	clock := newClock()
	m := NewMemory(clock.Now)
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		m.Allow(ctx, "k", free) // drain
	}

	clock.Add(90 * time.Millisecond)
	m.Allow(ctx, "k", free) // 0.9 tokens, rejected
	clock.Add(-90 * time.Millisecond)
	m.Allow(ctx, "k", free) // arrives "earlier"; must not rewind the bucket

	allowed := 0
	clock.Add(100 * time.Millisecond)
	if d, _ := m.Allow(ctx, "k", free); d.Allowed {
		allowed++
	}
	clock.Add(50 * time.Millisecond)
	if d, _ := m.Allow(ctx, "k", free); d.Allowed {
		allowed++
	}
	if allowed != 1 {
		t.Fatalf("allowed %d, want 1", allowed)
	}
}

func TestSweepNeverChangesDecisions(t *testing.T) {
	// The reason sweeping is safe: a swept bucket comes back full, so it
	// must only ever remove buckets that are already full.
	ctx := context.Background()
	swept, kept := NewMemory(nil), NewMemory(nil)
	clock := newClock()
	swept.now, kept.now = clock.Now, clock.Now

	steps := []time.Duration{0, 0, 0, 30 * time.Millisecond, 400 * time.Millisecond,
		time.Second, 1100 * time.Millisecond, 2 * time.Second, 5 * time.Millisecond}
	for i, step := range steps {
		clock.Add(step)
		swept.Sweep()
		a, _ := swept.Allow(ctx, "k", free)
		b, _ := kept.Allow(ctx, "k", free)
		if a != b {
			t.Fatalf("step %d: swept %+v, kept %+v", i, a, b)
		}
	}
}

func TestClockIsReadUnderTheLock(t *testing.T) {
	// If the clock were read before taking the lock, request A could read
	// an old time, stall, and apply it after B. Here A's clock read blocks;
	// B for the same key must wait for A instead of overtaking it.
	inClock := make(chan struct{})
	release := make(chan struct{})
	var first atomic.Bool
	m := NewMemory(func() time.Time {
		if first.CompareAndSwap(false, true) {
			close(inClock)
			<-release
		}
		return time.Unix(1_700_000_000, 0)
	})
	ctx := context.Background()

	aDone := make(chan struct{})
	go func() { m.Allow(ctx, "k", free); close(aDone) }()
	<-inClock

	bDone := make(chan struct{})
	go func() { m.Allow(ctx, "k", free); close(bDone) }()

	select {
	case <-bDone:
		t.Fatal("B finished while A was still reading the clock")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	<-aDone
	<-bDone
}
