package service

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"lighthouse/internal/model"
	"lighthouse/internal/source"
)

// Tuesday 10 Nov 2026, 15:00 UTC.
var now = time.Date(2026, 11, 10, 15, 0, 0, 0, time.UTC)

func date(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

func ptr[T any](v T) *T { return &v }

func fixture() model.Snapshot {
	return model.Snapshot{
		Accounts: []model.Account{
			{ID: "a", Name: "Alpha", MRR: 1000, RenewalDate: date("2026-11-30")},
			{ID: "b", Name: "Beta", MRR: 5000, RenewalDate: date("2027-06-01")},
		},
		Tickets: []model.Ticket{
			{ID: "1", AccountID: "a", Priority: "low", Status: "closed", OpenedAt: date("2026-10-01"), ClosedAt: ptr(date("2026-10-02"))},
			{ID: "2", AccountID: "a", Priority: "urgent", Status: "open", OpenedAt: date("2026-11-09")},
			{ID: "3", AccountID: "a", Priority: "normal", Status: "pending", OpenedAt: date("2026-11-02")},
		},
		Usage: []model.UsagePoint{
			// deliberately out of order: BuildDetails must sort by week
			{AccountID: "a", WeekStart: date("2026-11-02"), ActiveUsers: 50},
			{AccountID: "a", WeekStart: date("2026-10-05"), ActiveUsers: 100},
			{AccountID: "b", WeekStart: date("2026-10-05"), ActiveUsers: 10},
			{AccountID: "b", WeekStart: date("2026-11-02"), ActiveUsers: 12},
		},
	}
}

func TestBuildDetails(t *testing.T) {
	ds := BuildDetails(fixture(), now)
	if len(ds) != 2 {
		t.Fatalf("got %d details", len(ds))
	}
	a := ds[0]
	if a.ID != "a" {
		t.Fatalf("worst-health account should be first, got %s", a.ID)
	}
	if a.OpenTickets != 2 || a.UrgentOpen != 1 {
		t.Errorf("open=%d urgent=%d, want 2 and 1 (pending counts as open)", a.OpenTickets, a.UrgentOpen)
	}
	if a.DaysToRenewal != 20 {
		t.Errorf("DaysToRenewal = %d, want 20", a.DaysToRenewal)
	}
	if a.Usage[0] != 100 || a.Usage[1] != 50 || a.UsageChangePct != -50 {
		t.Errorf("usage = %v (%v%%), want [100 50] (-50%%)", a.Usage, a.UsageChangePct)
	}
	if a.Tickets[0].ID != "2" || a.Tickets[2].ID != "1" {
		t.Errorf("tickets should be urgent-open first, closed last: %v", ids(a.Tickets))
	}
	// 100 - 12 (2 open) - 15 (urgent) - 25 (usage -50%) = 48
	if a.Health != 48 || a.HealthLabel != model.LabelAtRisk {
		t.Errorf("health = %d %s, want 48 at-risk", a.Health, a.HealthLabel)
	}
	if b := ds[1]; b.Tickets == nil || len(b.Tickets) != 0 {
		t.Errorf("accounts without tickets should have an empty, non-nil slice")
	}
}

func TestBuildSummary(t *testing.T) {
	sum := BuildSummary(BuildDetails(fixture(), now), now)
	if sum.TotalMRR != 6000 || sum.MRRAtRisk != 1000 {
		t.Errorf("mrr total=%d atRisk=%d", sum.TotalMRR, sum.MRRAtRisk)
	}
	if sum.ByLabel[model.LabelAtRisk] != 1 || sum.ByLabel[model.LabelHealthy] != 1 {
		t.Errorf("by label = %v", sum.ByLabel)
	}
	if len(sum.Weekly) != 8 {
		t.Fatalf("weekly buckets = %d, want 8", len(sum.Weekly))
	}
	last := sum.Weekly[7]
	if !last.WeekStart.Equal(date("2026-11-09")) {
		t.Errorf("last bucket starts %v, want Monday 2026-11-09", last.WeekStart)
	}
	if last.Opened != 1 {
		t.Errorf("this week opened = %d, want 1", last.Opened)
	}
	if sum.Weekly[6].Opened != 1 {
		t.Errorf("last week opened = %d, want 1", sum.Weekly[6].Opened)
	}
	// The window starts Monday 2026-09-21, so all three tickets fall inside it.
	var opened int
	for _, w := range sum.Weekly {
		opened += w.Opened
	}
	if opened != 3 {
		t.Errorf("total opened in window = %d, want 3", opened)
	}
}

func TestWeekStart(t *testing.T) {
	cases := map[string]string{
		"2026-11-09": "2026-11-09", // Monday
		"2026-11-10": "2026-11-09", // Tuesday
		"2026-11-15": "2026-11-09", // Sunday
		"2026-11-16": "2026-11-16", // next Monday
	}
	for in, want := range cases {
		if got := WeekStart(date(in)); !got.Equal(date(want)) {
			t.Errorf("WeekStart(%s) = %s, want %s", in, got.Format("2006-01-02"), want)
		}
	}
}

// flaky fails whenever fail is set.
type flaky struct {
	fail atomic.Bool
	snap model.Snapshot
}

func (f *flaky) Name() string { return "flaky" }
func (f *flaky) Fetch(context.Context) (model.Snapshot, error) {
	if f.fail.Load() {
		return model.Snapshot{}, errors.New("down")
	}
	return f.snap, nil
}

func TestRefreshKeepsStaleDataWhenAllSourcesFail(t *testing.T) {
	src := &flaky{snap: fixture()}
	svc := New(Config{
		Sources: []source.Source{src},
		Now:     func() time.Time { return now },
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if ready, _ := svc.Ready(); ready {
		t.Fatal("should not be ready before first refresh")
	}
	if err := svc.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	v1 := svc.Version()

	src.fail.Store(true)
	if err := svc.Refresh(context.Background()); !errors.Is(err, ErrAllSourcesFailed) {
		t.Fatalf("err = %v, want ErrAllSourcesFailed", err)
	}
	if got := len(svc.Accounts(Query{})); got != 2 {
		t.Fatalf("previous data should be kept, got %d accounts", got)
	}
	if ready, stale := svc.Ready(); !ready || !stale {
		t.Fatalf("ready=%v stale=%v, want true true", ready, stale)
	}
	if svc.Version() != v1 {
		t.Fatal("version should not change on failed refresh")
	}

	src.fail.Store(false)
	if err := svc.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, stale := svc.Ready(); stale {
		t.Fatal("stale should clear after a good refresh")
	}
}

func TestAccountsQuery(t *testing.T) {
	svc := New(Config{
		Sources: []source.Source{source.Static{SourceName: "s", Snapshot: fixture()}},
		Now:     func() time.Time { return now },
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err := svc.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := svc.Accounts(Query{Label: model.LabelHealthy}); len(got) != 1 || got[0].ID != "b" {
		t.Errorf("label filter: %v", got)
	}
	if got := svc.Accounts(Query{Text: "ALP"}); len(got) != 1 || got[0].ID != "a" {
		t.Errorf("text filter should be case-insensitive: %v", got)
	}
	if got := svc.Accounts(Query{Sort: "mrr"}); got[0].ID != "b" {
		t.Errorf("sort by mrr: first = %s", got[0].ID)
	}
	if _, ok := svc.Account("nope"); ok {
		t.Error("unknown account should not be found")
	}
}

func ids(ts []model.Ticket) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.ID
	}
	return out
}
