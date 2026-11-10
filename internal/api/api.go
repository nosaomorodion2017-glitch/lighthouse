// Package api exposes the platform over HTTP: a JSON API, operational
// endpoints, and the embedded dashboard.
package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"lighthouse/internal/insight"
	"lighthouse/internal/obs"
	"lighthouse/internal/service"
)

// Server wires HTTP handlers to the service.
type Server struct {
	svc     *service.Service
	briefer insight.Briefer
	metrics *obs.Metrics
	log     *slog.Logger
	static  fs.FS

	briefMu    sync.Mutex
	briefCache map[string]cachedBrief
}

type cachedBrief struct {
	version int64
	brief   insight.Brief
}

// New creates a Server. static may be nil (no dashboard).
func New(svc *service.Service, b insight.Briefer, m *obs.Metrics, log *slog.Logger, static fs.FS) *Server {
	return &Server{svc: svc, briefer: b, metrics: m, log: log, static: static, briefCache: map[string]cachedBrief{}}
}

// Handler returns the full HTTP handler with middleware applied.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /readyz", s.readyz)
	mux.HandleFunc("GET /metrics", s.metricsHandler)

	mux.HandleFunc("GET /api/summary", s.summary)
	mux.HandleFunc("GET /api/accounts", s.accounts)
	mux.HandleFunc("GET /api/accounts/{id}", s.account)
	mux.HandleFunc("POST /api/accounts/{id}/brief", s.brief)
	mux.HandleFunc("GET /api/sources", s.sources)
	mux.HandleFunc("POST /api/refresh", s.refresh)

	if s.static != nil {
		mux.Handle("GET /", http.FileServerFS(s.static))
	}
	return s.middleware(mux)
}

// ---------------------------------------------------------------------------
// Operational endpoints.

func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) readyz(w http.ResponseWriter, _ *http.Request) {
	ready, stale := s.svc.Ready()
	if !ready {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ready": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ready": true, "stale": stale})
}

func (s *Server) metricsHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	_, _ = s.metrics.WriteTo(w)
}

// ---------------------------------------------------------------------------
// API.

func (s *Server) summary(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.svc.Summary())
}

func (s *Server) accounts(w http.ResponseWriter, r *http.Request) {
	q := service.Query{
		Label: r.URL.Query().Get("label"),
		Text:  r.URL.Query().Get("q"),
		Sort:  r.URL.Query().Get("sort"),
	}
	if !service.ValidLabels[q.Label] {
		writeError(w, http.StatusBadRequest, "invalid_label", "label must be one of: healthy, watch, at-risk")
		return
	}
	if !service.ValidSorts[q.Sort] {
		writeError(w, http.StatusBadRequest, "invalid_sort", "sort must be one of: health, mrr, renewal, name")
		return
	}
	if len(q.Text) > 100 {
		writeError(w, http.StatusBadRequest, "invalid_query", "q must be at most 100 characters")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"accounts": s.svc.Accounts(q)})
}

func (s *Server) account(w http.ResponseWriter, r *http.Request) {
	d, ok := s.svc.Account(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "account not found")
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) brief(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	d, ok := s.svc.Account(id)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "account not found")
		return
	}

	// Cache per account per data version: the brief only changes when the
	// underlying facts do, so repeated clicks cost nothing.
	version := s.svc.Version()
	s.briefMu.Lock()
	c, hit := s.briefCache[id]
	s.briefMu.Unlock()
	if hit && c.version == version {
		s.metrics.Inc(`lighthouse_briefs_total{source="cache"}`)
		writeJSON(w, http.StatusOK, c.brief)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	b, err := s.briefer.Brief(ctx, d)
	if err != nil {
		s.log.Error("brief failed", "account", id, "error", err)
		writeError(w, http.StatusBadGateway, "brief_failed", "could not generate a brief")
		return
	}
	s.metrics.Inc(`lighthouse_briefs_total{source="` + b.Source + `"}`)
	if b.Note == "" || b.Source == "claude" {
		s.briefMu.Lock()
		s.briefCache[id] = cachedBrief{version: version, brief: b}
		s.briefMu.Unlock()
	}
	writeJSON(w, http.StatusOK, b)
}

func (s *Server) sources(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.svc.Sources())
}

func (s *Server) refresh(w http.ResponseWriter, r *http.Request) {
	err := s.svc.Refresh(r.Context())
	s.RecordSourceGauges()
	if errors.Is(err, service.ErrAllSourcesFailed) {
		writeError(w, http.StatusBadGateway, "all_sources_failed", err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "refresh_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.svc.Sources())
}

// RecordSourceGauges publishes per-source up/latency gauges.
func (s *Server) RecordSourceGauges() {
	for _, st := range s.svc.Sources().Sources {
		up := 0.0
		if st.OK {
			up = 1
		}
		name := strconv.Quote(st.Name)
		s.metrics.SetGauge("lighthouse_source_up{source="+name+"}", up)
		s.metrics.SetGauge("lighthouse_source_latency_ms{source="+name+"}", st.LatencyMS)
		s.metrics.SetGauge("lighthouse_source_records{source="+name+"}", float64(st.Records))
	}
}

// ---------------------------------------------------------------------------
// Middleware: request ID, security headers, panic recovery, logging, metrics.

func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		reqID := r.Header.Get("X-Request-ID")
		if reqID == "" || len(reqID) > 64 {
			reqID = newID()
		}
		h := w.Header()
		h.Set("X-Request-ID", reqID)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; frame-ancestors 'none'")

		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		defer func() {
			if p := recover(); p != nil {
				s.log.Error("panic", "error", p, "req_id", reqID)
				if !rec.wrote {
					writeError(rec, http.StatusInternalServerError, "internal", "internal error")
				}
			}
			route := r.Pattern
			if route == "" {
				route = "unmatched"
			}
			d := time.Since(start)
			s.metrics.ObserveRequest(route, rec.status, d)
			level := slog.LevelInfo
			if rec.status >= 500 {
				level = slog.LevelError
			}
			s.log.Log(r.Context(), level, "request",
				"req_id", reqID, "method", r.Method, "path", r.URL.Path, "route", route,
				"status", rec.status, "dur_ms", float64(d.Microseconds())/1000)
		}()
		next.ServeHTTP(rec, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (r *statusRecorder) WriteHeader(code int) {
	if !r.wrote {
		r.status, r.wrote = code, true
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	r.wrote = true
	return r.ResponseWriter.Write(b)
}

// ---------------------------------------------------------------------------
// Helpers.

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": msg}})
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
