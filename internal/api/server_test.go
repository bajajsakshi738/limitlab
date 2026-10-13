package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"limitlab/internal/apikey"
	"limitlab/internal/keys"
	"limitlab/internal/limiter"
	"limitlab/internal/plans"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

type testEnv struct {
	srv     *httptest.Server
	freeKey string
	proKey  string
	revoked string
	oddPlan string
	clock   *clock
}

func newKey(t *testing.T) string {
	t.Helper()
	k, err := apikey.Generate()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func newEnv(t *testing.T) *testEnv {
	t.Helper()
	e := &testEnv{
		freeKey: newKey(t), proKey: newKey(t), revoked: newKey(t), oddPlan: newKey(t),
		clock: &clock{t: time.Unix(1_700_000_000, 0)},
	}
	store, err := keys.NewFileStore([]keys.Key{
		{ID: 1, Hash: apikey.Hash(e.freeKey), Prefix: apikey.Display(e.freeKey), Plan: "free"},
		{ID: 2, Hash: apikey.Hash(e.proKey), Prefix: apikey.Display(e.proKey), Plan: "pro"},
		{ID: 3, Hash: apikey.Hash(e.revoked), Prefix: apikey.Display(e.revoked), Plan: "free", Revoked: true},
		// startup rejects unknown plans; this checks the handler copes anyway
		{ID: 4, Hash: apikey.Hash(e.oddPlan), Prefix: apikey.Display(e.oddPlan), Plan: "gold"},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{
		Keys:     store,
		Plans:    plans.Defaults(),
		Limiter:  limiter.NewMemory(e.clock.Now),
		Instance: "api-test",
		Log:      quiet,
	}
	e.srv = httptest.NewServer(s.Handler())
	t.Cleanup(e.srv.Close)
	return e
}

func (e *testEnv) get(t *testing.T, path, auth string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("GET", e.srv.URL+path, nil)
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp
}

func TestUnauthorized(t *testing.T) {
	e := newEnv(t)
	cases := map[string]string{
		"no header":       "",
		"wrong scheme":    "Basic " + e.freeKey,
		"bad checksum":    "Bearer " + mangle(e.freeKey),
		"garbage":         "Bearer hello",
		"unknown key":     "Bearer " + newKey(t),
		"revoked key":     "Bearer " + e.revoked,
		"empty bearer":    "Bearer ",
		"key in two bits": "Bearer " + e.freeKey[:20] + " " + e.freeKey[20:],
		"no space":        "Bearer" + e.freeKey,
	}
	for name, auth := range cases {
		resp := e.get(t, "/api/data", auth)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: status %d, want 401", name, resp.StatusCode)
		}
		if resp.Header.Get("WWW-Authenticate") != "Bearer" {
			t.Errorf("%s: missing WWW-Authenticate", name)
		}
		if resp.Header.Get("RateLimit") != "" {
			t.Errorf("%s: 401 should not carry rate limit headers", name)
		}
	}
}

func TestKeyInQueryIsIgnored(t *testing.T) {
	e := newEnv(t)
	resp := e.get(t, "/api/data?api_key="+e.freeKey, "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", resp.StatusCode)
	}
}

func TestBearerSpellings(t *testing.T) {
	e := newEnv(t)
	for _, auth := range []string{"bearer " + e.freeKey, "Bearer   " + e.freeKey, "Bearer " + e.freeKey + " "} {
		if resp := e.get(t, "/api/data", auth); resp.StatusCode != 200 {
			t.Errorf("%q: status %d", auth, resp.StatusCode)
		}
	}
}

func TestFreePlanLimitAndHeaders(t *testing.T) {
	e := newEnv(t)
	auth := "Bearer " + e.freeKey

	resp := e.get(t, "/api/data", auth)
	if resp.StatusCode != 200 {
		t.Fatalf("first request: %d", resp.StatusCode)
	}
	if got := resp.Header.Get("RateLimit-Policy"); got != `"free";q=10;w=1` {
		t.Errorf("policy header %q", got)
	}
	if got := resp.Header.Get("RateLimit"); got != `"free";r=9;t=1` {
		t.Errorf("ratelimit header %q", got)
	}
	if resp.Header.Get("Retry-After") != "" {
		t.Error("Retry-After on a 200")
	}
	if resp.Header.Get("X-Instance") != "api-test" {
		t.Error("missing X-Instance")
	}

	for i := 0; i < 9; i++ {
		if resp := e.get(t, "/api/data", auth); resp.StatusCode != 200 {
			t.Fatalf("request %d: %d", i+2, resp.StatusCode)
		}
	}

	resp = e.get(t, "/api/data", auth)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("11th request: %d, want 429", resp.StatusCode)
	}
	if resp.Header.Get("Retry-After") != "1" {
		t.Errorf("Retry-After %q", resp.Header.Get("Retry-After"))
	}
	if got := resp.Header.Get("RateLimit"); got != `"free";r=0;t=1` {
		t.Errorf("ratelimit header on 429 %q", got)
	}

	e.clock.Add(100 * time.Millisecond)
	if resp := e.get(t, "/api/data", auth); resp.StatusCode != 200 {
		t.Fatalf("after refill: %d", resp.StatusCode)
	}
}

func TestProPlanIsSeparate(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 11; i++ {
		e.get(t, "/api/data", "Bearer "+e.freeKey)
	}
	resp := e.get(t, "/api/data", "Bearer "+e.proKey)
	if resp.StatusCode != 200 {
		t.Fatalf("pro key: %d", resp.StatusCode)
	}
	if got := resp.Header.Get("RateLimit-Policy"); got != `"pro";q=100;w=1` {
		t.Errorf("pro policy %q", got)
	}
	if got := resp.Header.Get("RateLimit"); got != `"pro";r=99;t=1` {
		t.Errorf("pro ratelimit %q", got)
	}
}

func TestUnknownPlanIs500(t *testing.T) {
	e := newEnv(t)
	if resp := e.get(t, "/api/data", "Bearer "+e.oddPlan); resp.StatusCode != 500 {
		t.Fatalf("status %d, want 500", resp.StatusCode)
	}
}

func TestHealthzNeedsNoKey(t *testing.T) {
	e := newEnv(t)
	if resp := e.get(t, "/healthz", ""); resp.StatusCode != 200 {
		t.Fatalf("healthz %d", resp.StatusCode)
	}
}

func TestWrongMethod(t *testing.T) {
	e := newEnv(t)
	resp, err := http.Post(e.srv.URL+"/api/data", "text/plain", strings.NewReader("x"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST got %d", resp.StatusCode)
	}
}

type failingStore struct{}

func (failingStore) Lookup(context.Context, string) (keys.Key, error) {
	return keys.Key{}, errors.New("db down")
}

type failingLimiter struct{}

func (failingLimiter) Allow(context.Context, string, limiter.Rule) (limiter.Decision, error) {
	return limiter.Decision{}, errors.New("redis down")
}

func serve(s *Server, auth string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/data", nil)
	req.Header.Set("Authorization", auth)
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func TestStoreErrorIs503Not401(t *testing.T) {
	s := &Server{Keys: failingStore{}, Plans: plans.Defaults(), Limiter: limiter.NewMemory(nil), Log: quiet}
	if rec := serve(s, "Bearer "+newKey(t)); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503", rec.Code)
	}
}

func TestLimiterErrorIs503(t *testing.T) {
	k := newKey(t)
	store, _ := keys.NewFileStore([]keys.Key{{ID: 1, Hash: apikey.Hash(k), Plan: "free"}})
	s := &Server{Keys: store, Plans: plans.Defaults(), Limiter: failingLimiter{}, Log: quiet}
	rec := serve(s, "Bearer "+k)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "redis") {
		t.Fatal("internal error text leaked to the client")
	}
}

func TestPanicIsRecovered(t *testing.T) {
	s := &Server{Log: quiet}
	h := s.recover(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("RateLimit", `"free";r=3;t=1`)
		panic("boom")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 500 || strings.Contains(rec.Body.String(), "boom") {
		t.Fatalf("status %d body %q", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("RateLimit") != "" {
		t.Fatal("headers from the failed response leaked into the 500")
	}
}

func TestPanicAfterWritingAborts(t *testing.T) {
	s := &Server{Log: quiet}
	srv := httptest.NewServer(s.recover(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte("partial"))
		panic("boom")
	})))
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err == nil {
		_, err = io.ReadAll(resp.Body)
		resp.Body.Close()
	}
	if err == nil {
		t.Fatal("client got a clean response after a mid-response panic")
	}
}

func TestServerLimitsAreSet(t *testing.T) {
	s := NewHTTPServer(":0", http.NotFoundHandler())
	if s.ReadHeaderTimeout == 0 || s.ReadTimeout == 0 || s.WriteTimeout == 0 || s.IdleTimeout == 0 {
		t.Fatal("a server timeout is missing")
	}
	if s.MaxHeaderBytes == 0 || s.MaxHeaderBytes > 64<<10 {
		t.Fatalf("MaxHeaderBytes %d", s.MaxHeaderBytes)
	}
}

// mangle changes the last character so the key is almost but not quite right.
func mangle(key string) string {
	last := byte('x')
	if key[len(key)-1] == 'x' {
		last = 'y'
	}
	return key[:len(key)-1] + string(last)
}
