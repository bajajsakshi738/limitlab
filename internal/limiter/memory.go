package limiter

import (
	"context"
	"sync"
	"time"
)

// Memory keeps buckets inside this process. That's correct for a single
// instance and wrong as soon as there are several behind a load balancer:
// each one hands out the full limit.
type Memory struct {
	now    func() time.Time
	shards [32]shard
}

// Sharding the map means two different keys rarely wait on the same lock.
type shard struct {
	mu sync.Mutex
	m  map[string]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
	fullAt time.Time // after this the bucket is full and can be forgotten
}

// NewMemory takes a clock so tests can move time by hand.
func NewMemory(now func() time.Time) *Memory {
	if now == nil {
		now = time.Now
	}
	m := &Memory{now: now}
	for i := range m.shards {
		m.shards[i].m = make(map[string]*bucket)
	}
	return m
}

func (m *Memory) Allow(_ context.Context, key string, r Rule) (Decision, error) {
	if err := r.Validate(); err != nil {
		return Decision{}, err
	}

	// The rule name is part of the key so a key that changes plan starts
	// with a fresh bucket instead of inheriting the old one.
	k := r.Name + ":" + key
	s := &m.shards[shardFor(k)]

	s.mu.Lock()
	defer s.mu.Unlock()

	// Read the clock while holding the lock. Read before it, a request that
	// waited for the lock could carry an older time than the one that went
	// first, and refill the same stretch of time twice.
	now := m.now()

	b, ok := s.m[k]
	if !ok {
		b = &bucket{tokens: float64(r.Burst), last: now}
		s.m[k] = b
	}
	tokens, d := take(b.tokens, now.Sub(b.last), r)
	b.tokens = tokens
	if now.After(b.last) {
		b.last = now
	}
	b.fullAt = b.last.Add(ceilMillis((float64(r.Burst) - tokens) / r.Rate))
	return d, nil
}

// Sweep drops buckets that have been full for a while. Forgetting a full
// bucket changes nothing, because a missing bucket starts full anyway.
func (m *Memory) Sweep() (removed int) {
	cutoff := m.now().Add(-time.Second)
	for i := range m.shards {
		s := &m.shards[i]
		s.mu.Lock()
		for k, b := range s.m {
			if b.fullAt.Before(cutoff) {
				delete(s.m, k)
				removed++
			}
		}
		s.mu.Unlock()
	}
	return removed
}

// Len is the number of buckets currently held (for tests and stats).
func (m *Memory) Len() int {
	n := 0
	for i := range m.shards {
		s := &m.shards[i]
		s.mu.Lock()
		n += len(s.m)
		s.mu.Unlock()
	}
	return n
}

// RunSweeper sweeps every interval until ctx is cancelled.
func (m *Memory) RunSweeper(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.Sweep()
		}
	}
}

// shardFor is FNV-1a written out by hand, so it doesn't allocate on
// every request the way hash/fnv plus a []byte conversion would.
func shardFor(k string) uint32 {
	h := uint32(2166136261)
	for i := 0; i < len(k); i++ {
		h ^= uint32(k[i])
		h *= 16777619
	}
	return h % 32
}
