package aggregate

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"lighthouse/internal/model"
	"lighthouse/internal/source"
)

var fixedNow = func() time.Time { return time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC) }

type panicky struct{}

func (panicky) Name() string { return "panicky" }
func (panicky) Fetch(context.Context) (model.Snapshot, error) {
	panic("boom")
}

// ignoresCtx sleeps without watching its context, like a badly written client.
type ignoresCtx struct{}

func (ignoresCtx) Name() string { return "stuck" }
func (ignoresCtx) Fetch(context.Context) (model.Snapshot, error) {
	time.Sleep(2 * time.Second)
	return model.Snapshot{}, nil
}

func crm(ids ...string) source.Static {
	var accts []model.Account
	for _, id := range ids {
		accts = append(accts, model.Account{ID: id, Name: id})
	}
	return source.Static{SourceName: "crm", Snapshot: model.Snapshot{Accounts: accts}}
}

func TestCollectMergesAllSources(t *testing.T) {
	tickets := source.Static{SourceName: "support", Snapshot: model.Snapshot{Tickets: []model.Ticket{{ID: "t1", AccountID: "a"}}}}
	res := Collect(context.Background(), []source.Source{crm("a", "b"), tickets}, time.Second, fixedNow)
	if len(res.Snapshot.Accounts) != 2 || len(res.Snapshot.Tickets) != 1 {
		t.Fatalf("unexpected snapshot: %+v", res.Snapshot)
	}
	for _, st := range res.Statuses {
		if !st.OK {
			t.Errorf("%s not ok: %s", st.Name, st.Error)
		}
	}
}

func TestPartialFailureIsReported(t *testing.T) {
	bad := source.Static{SourceName: "support", Err: errors.New("connection refused")}
	res := Collect(context.Background(), []source.Source{crm("a"), bad}, time.Second, fixedNow)
	if len(res.Snapshot.Accounts) != 1 {
		t.Fatal("healthy source data should still be merged")
	}
	if res.Statuses[1].OK || !strings.Contains(res.Statuses[1].Error, "connection refused") {
		t.Fatalf("failure not reported: %+v", res.Statuses[1])
	}
	if res.AllFailed() {
		t.Fatal("AllFailed should be false")
	}
}

func TestSlowSourceTimesOutWithoutBlockingOthers(t *testing.T) {
	start := time.Now()
	res := Collect(context.Background(), []source.Source{crm("a"), ignoresCtx{}}, 50*time.Millisecond, fixedNow)
	if el := time.Since(start); el > time.Second {
		t.Fatalf("Collect took %v; timeout not enforced", el)
	}
	if res.Statuses[1].OK || !strings.Contains(res.Statuses[1].Error, "deadline") {
		t.Fatalf("expected deadline error, got %+v", res.Statuses[1])
	}
	if !res.Statuses[0].OK {
		t.Fatal("fast source should succeed")
	}
}

func TestPanicIsRecovered(t *testing.T) {
	res := Collect(context.Background(), []source.Source{crm("a"), panicky{}}, time.Second, fixedNow)
	if res.Statuses[1].OK || !strings.Contains(res.Statuses[1].Error, "panic") {
		t.Fatalf("panic not converted to error: %+v", res.Statuses[1])
	}
}

func TestOrphansAreDroppedAndWarned(t *testing.T) {
	support := source.Static{SourceName: "support", Snapshot: model.Snapshot{Tickets: []model.Ticket{
		{ID: "t1", AccountID: "a"}, {ID: "t2", AccountID: "ghost"}, {ID: "t3", AccountID: "ghost"},
	}}}
	res := Collect(context.Background(), []source.Source{crm("a"), support}, time.Second, fixedNow)
	if len(res.Snapshot.Tickets) != 1 {
		t.Fatalf("got %d tickets, want 1", len(res.Snapshot.Tickets))
	}
	want := `dropped 2 ticket(s) for unknown account "ghost"`
	if len(res.Warnings) != 1 || res.Warnings[0] != want {
		t.Fatalf("warnings = %q, want [%q]", res.Warnings, want)
	}
}

func TestAllFailed(t *testing.T) {
	bad := source.Static{SourceName: "x", Err: errors.New("down")}
	if !Collect(context.Background(), []source.Source{bad}, time.Second, fixedNow).AllFailed() {
		t.Fatal("expected AllFailed")
	}
}
