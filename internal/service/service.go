// Package service owns the current platform state: it refreshes data from
// the sources and serves read-only views of the latest good snapshot.
package service

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"lighthouse/internal/aggregate"
	"lighthouse/internal/model"
	"lighthouse/internal/source"
)

// ErrAllSourcesFailed is returned by Refresh when no source succeeded. The
// previous snapshot (if any) is kept, so users see stale data rather than none.
var ErrAllSourcesFailed = errors.New("all sources failed; keeping previous data")

// Config configures a Service.
type Config struct {
	Sources []source.Source
	Timeout time.Duration    // per-source fetch timeout
	Now     func() time.Time // injectable clock for tests
	Logger  *slog.Logger
}

// Service is safe for concurrent use.
type Service struct {
	cfg Config

	mu    sync.RWMutex
	state state
}

type state struct {
	details     []model.AccountDetail
	byID        map[string]int
	summary     model.Summary
	statuses    []model.SourceStatus
	warnings    []string
	refreshedAt time.Time // time of the last refresh that produced data
	stale       bool      // true when the latest refresh failed entirely
	version     int64     // increments on every successful refresh
}

// New creates a Service. Call Refresh before serving.
func New(cfg Config) *Service {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 5 * time.Second
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Service{cfg: cfg}
}

// Refresh collects from all sources and swaps in the new state atomically.
func (s *Service) Refresh(ctx context.Context) error {
	res := aggregate.Collect(ctx, s.cfg.Sources, s.cfg.Timeout, s.cfg.Now)
	for _, st := range res.Statuses {
		if !st.OK {
			s.cfg.Logger.Warn("source failed", "source", st.Name, "error", st.Error)
		}
	}
	for _, w := range res.Warnings {
		s.cfg.Logger.Warn("data integrity", "warning", w)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if res.AllFailed() {
		s.state.statuses = res.Statuses
		s.state.stale = true
		return ErrAllSourcesFailed
	}

	now := s.cfg.Now()
	details := BuildDetails(res.Snapshot, now)
	byID := make(map[string]int, len(details))
	for i, d := range details {
		byID[d.ID] = i
	}
	sum := BuildSummary(details, now)
	sum.RefreshedAt = now

	s.state = state{
		details:     details,
		byID:        byID,
		summary:     sum,
		statuses:    res.Statuses,
		warnings:    res.Warnings,
		refreshedAt: now,
		version:     s.state.version + 1,
	}
	s.cfg.Logger.Info("refreshed", "accounts", len(details), "warnings", len(res.Warnings))
	return nil
}

// Run refreshes on every tick until ctx is cancelled.
func (s *Service) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.Refresh(ctx); err != nil {
				s.cfg.Logger.Error("scheduled refresh failed", "error", err)
			}
		}
	}
}

// Query filters and sorts the account list.
type Query struct {
	Label string // "" or a health label
	Text  string // case-insensitive match on name, owner, industry
	Sort  string // health (default) | mrr | renewal | name
}

// ValidSorts and ValidLabels are the accepted query values.
var (
	ValidSorts  = map[string]bool{"": true, "health": true, "mrr": true, "renewal": true, "name": true}
	ValidLabels = map[string]bool{"": true, model.LabelHealthy: true, model.LabelWatch: true, model.LabelAtRisk: true}
)

// Accounts returns account views matching q.
func (s *Service) Accounts(q Query) []model.AccountView {
	s.mu.RLock()
	defer s.mu.RUnlock()
	text := strings.ToLower(strings.TrimSpace(q.Text))
	out := []model.AccountView{}
	for _, d := range s.state.details {
		if q.Label != "" && d.HealthLabel != q.Label {
			continue
		}
		if text != "" && !strings.Contains(strings.ToLower(d.Name+" "+d.Owner+" "+d.Industry), text) {
			continue
		}
		out = append(out, d.AccountView)
	}
	sortViews(out, q.Sort)
	return out
}

// Account returns the full detail for one account.
func (s *Service) Account(id string) (model.AccountDetail, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	i, ok := s.state.byID[id]
	if !ok {
		return model.AccountDetail{}, false
	}
	return s.state.details[i], true
}

// Summary returns portfolio-level numbers.
func (s *Service) Summary() model.Summary {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state.summary
}

// SourcesReport describes the health of the data pipeline.
type SourcesReport struct {
	Sources     []model.SourceStatus `json:"sources"`
	Warnings    []string             `json:"warnings"`
	RefreshedAt time.Time            `json:"refreshed_at"`
	Stale       bool                 `json:"stale"`
}

// Sources returns the status of the last refresh.
func (s *Service) Sources() SourcesReport {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r := SourcesReport{
		Sources:     s.state.statuses,
		Warnings:    s.state.warnings,
		RefreshedAt: s.state.refreshedAt,
		Stale:       s.state.stale,
	}
	if r.Warnings == nil {
		r.Warnings = []string{}
	}
	if r.Sources == nil {
		r.Sources = []model.SourceStatus{}
	}
	return r
}

// Ready reports whether the service has data to serve.
func (s *Service) Ready() (ready, stale bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state.version > 0, s.state.stale
}

// Version changes every time new data is loaded. Callers use it as a cache key.
func (s *Service) Version() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state.version
}

func sortViews(v []model.AccountView, by string) {
	less := map[string]func(a, b model.AccountView) bool{
		"mrr":     func(a, b model.AccountView) bool { return a.MRR > b.MRR },
		"renewal": func(a, b model.AccountView) bool { return a.RenewalDate.Before(b.RenewalDate) },
		"name":    func(a, b model.AccountView) bool { return a.Name < b.Name },
	}[by]
	if less == nil {
		return // already in health order
	}
	// Stable so ties keep the default health ordering.
	sort.SliceStable(v, func(i, j int) bool { return less(v[i], v[j]) })
}
