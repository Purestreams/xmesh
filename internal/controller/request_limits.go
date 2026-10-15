package controller

import (
	"net"
	"net/http"
	"strings"
	"time"
)

type requestBucket struct {
	tokens float64
	at     time.Time
}

// Rate-limit before parsing bodies or scanning credentials. Forwarding headers
// are deliberately ignored: only the actual TCP peer is a trusted identity.
func (s *Server) limitPublicRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		group, rate, burst := "", 100.0, 200.0
		switch {
		case r.URL.Path == "/api/v1/enroll" || r.URL.Path == "/api/v1/updater/pair":
			group, rate, burst = "enroll", 1, 10
		case strings.HasPrefix(r.URL.Path, "/api/v1/"):
			group = "node"
		case strings.HasPrefix(r.URL.Path, "/subscription/"):
			group, rate, burst = "subscription", 5, 20
		default:
			next.ServeHTTP(w, r)
			return
		}
		peer, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			peer = r.RemoteAddr
		}
		key, now := group+"/"+peer, s.now()
		s.requestMu.Lock()
		if s.requestSources == nil {
			s.requestSources = make(map[string]requestBucket)
		}
		bucket, exists := s.requestSources[key]
		if !exists && len(s.requestSources) >= 4096 && (s.requestSweepAt.IsZero() || now.Sub(s.requestSweepAt) >= time.Minute) {
			s.requestSweepAt = now
			for source, entry := range s.requestSources {
				if now.Sub(entry.at) >= 10*time.Minute {
					delete(s.requestSources, source)
				}
			}
		}
		allowed := s.requestActive < 64 && (exists || len(s.requestSources) < 4096)
		if !exists {
			bucket = requestBucket{tokens: burst, at: now}
		}
		bucket.tokens = min(burst, bucket.tokens+max(0, now.Sub(bucket.at).Seconds())*rate)
		bucket.at = now
		allowed = allowed && bucket.tokens >= 1
		if allowed {
			bucket.tokens--
			s.requestActive++
		}
		if exists || len(s.requestSources) < 4096 {
			s.requestSources[key] = bucket
		}
		s.requestMu.Unlock()
		if !allowed {
			w.Header().Set("Retry-After", "1")
			http.Error(w, "too many requests; retry later", http.StatusTooManyRequests)
			return
		}
		defer func() { s.requestMu.Lock(); s.requestActive--; s.requestMu.Unlock() }()
		next.ServeHTTP(w, r)
	})
}
