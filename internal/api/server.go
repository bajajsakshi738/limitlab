// Package api is the HTTP side of the rate-limited service.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"limitlab/internal/apikey"
	"limitlab/internal/keys"
	"limitlab/internal/limiter"
)

type Server struct {
	Keys     keys.Store
	Plans    map[string]limiter.Rule
	Limiter  limiter.Limiter
	Instance string
	Log      *slog.Logger
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /api/data", s.authenticate(s.rateLimit(http.HandlerFunc(s.data))))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok\n"))
	})
	return s.recover(mux)
}

// NewHTTPServer wraps h with timeouts. Go's zero-value server has none,
// so a client sending headers one byte a minute could hold a connection
// open forever (slowloris).
func NewHTTPServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
}

type ctxKey struct{}

func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only the header is accepted. Keys in URLs end up in proxy logs
		// and browser history.
		raw, ok := bearer(r.Header.Get("Authorization"))
		if !ok || !apikey.WellFormed(raw) {
			unauthorized(w)
			return
		}
		k, err := s.Keys.Lookup(r.Context(), apikey.Hash(raw))
		switch {
		case errors.Is(err, keys.ErrNotFound):
			unauthorized(w)
			return
		case err != nil:
			// The store being down is not the client's fault, so no 401.
			s.Log.Error("key lookup failed", "err", err, "key", apikey.Display(raw))
			writeJSON(w, http.StatusServiceUnavailable, "temporarily unavailable")
			return
		case k.Revoked:
			unauthorized(w)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, k)))
	})
}

func (s *Server) rateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		k := r.Context().Value(ctxKey{}).(keys.Key)
		rule, ok := s.Plans[k.Plan]
		if !ok {
			s.Log.Error("key has unknown plan", "key", k.Prefix, "plan", k.Plan)
			writeJSON(w, http.StatusInternalServerError, "internal error")
			return
		}

		d, err := s.Limiter.Allow(r.Context(), strconv.FormatInt(k.ID, 10), rule)
		if err != nil {
			s.Log.Error("limiter failed", "err", err, "key", k.Prefix)
			writeJSON(w, http.StatusServiceUnavailable, "temporarily unavailable")
			return
		}

		w.Header().Set("X-Instance", s.Instance)
		setRateHeaders(w.Header(), rule.Name, d)
		if !d.Allowed {
			writeJSON(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) data(w http.ResponseWriter, r *http.Request) {
	k := r.Context().Value(ctxKey{}).(keys.Key)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"message":  "hello from " + s.Instance,
		"plan":     k.Plan,
		"instance": s.Instance,
	})
}

// setRateHeaders follows draft-ietf-httpapi-ratelimit-headers-10:
//
//	RateLimit-Policy: "free";q=10;w=1   quota q per w seconds
//	RateLimit: "free";r=7;t=1           r left now, t seconds until more
func setRateHeaders(h http.Header, policy string, d limiter.Decision) {
	w := max(1, ceilSeconds(d.Window))
	t := ceilSeconds(d.Wait)
	h.Set("RateLimit-Policy", `"`+policy+`";q=`+strconv.Itoa(d.Quota)+";w="+strconv.Itoa(w))
	h.Set("RateLimit", `"`+policy+`";r=`+strconv.Itoa(d.Remaining)+";t="+strconv.Itoa(t))
	if !d.Allowed {
		h.Set("Retry-After", strconv.Itoa(max(1, t)))
	}
}

func ceilSeconds(d time.Duration) int {
	return int(math.Ceil(d.Seconds()))
}

func bearer(header string) (string, bool) {
	const p = "Bearer "
	if len(header) <= len(p) || !strings.EqualFold(header[:len(p)], p) {
		return "", false
	}
	return strings.TrimSpace(header[len(p):]), true
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	writeJSON(w, http.StatusUnauthorized, "missing or invalid API key")
}

func writeJSON(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func (s *Server) recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tw := &trackingWriter{ResponseWriter: w}
		defer func() {
			v := recover()
			if v == nil {
				return
			}
			if v == http.ErrAbortHandler {
				panic(v)
			}
			s.Log.Error("panic", "value", v, "path", r.URL.Path)
			if tw.wrote {
				// Too late for a clean 500; abort so the client sees a broken
				// response rather than a half-written "success".
				panic(http.ErrAbortHandler)
			}
			// drop rate limit headers already set for the failed response
			h := w.Header()
			clear(h)
			setSafetyHeaders(h)
			writeJSON(w, http.StatusInternalServerError, "internal error")
		}()
		setSafetyHeaders(w.Header())
		next.ServeHTTP(tw, r)
	})
}

// Nothing this API returns is meant to be cached or sniffed as HTML.
func setSafetyHeaders(h http.Header) {
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-store")
}

// trackingWriter remembers whether the response has started.
type trackingWriter struct {
	http.ResponseWriter
	wrote bool
}

func (t *trackingWriter) WriteHeader(code int) {
	t.wrote = true
	t.ResponseWriter.WriteHeader(code)
}

func (t *trackingWriter) Write(b []byte) (int, error) {
	t.wrote = true
	return t.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the real writer.
func (t *trackingWriter) Unwrap() http.ResponseWriter { return t.ResponseWriter }
