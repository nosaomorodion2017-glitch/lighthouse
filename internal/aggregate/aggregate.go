// Package aggregate fetches every source concurrently and merges the results.
//
// Guarantees:
//   - one slow source cannot block the others (per-source timeout, enforced
//     even if the source ignores its context);
//   - one crashing source cannot take down the process (panics are recovered);
//   - nothing fails silently: every source gets a status, and records that
//     point at unknown accounts are dropped and reported as warnings.
package aggregate

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"lighthouse/internal/model"
	"lighthouse/internal/source"
)

// Result is the merged output of one collection run.
type Result struct {
	Snapshot model.Snapshot
	Statuses []model.SourceStatus
	Warnings []string
}

// AllFailed reports whether no source succeeded.
func (r Result) AllFailed() bool {
	for _, s := range r.Statuses {
		if s.OK {
			return false
		}
	}
	return true
}

// Collect fetches all sources concurrently, each bounded by timeout.
func Collect(ctx context.Context, sources []source.Source, timeout time.Duration, now func() time.Time) Result {
	type outcome struct {
		snap model.Snapshot
		err  error
		dur  time.Duration
	}
	outs := make([]outcome, len(sources))

	var wg sync.WaitGroup
	for i, s := range sources {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			start := time.Now()
			snap, err := fetch(cctx, s)
			outs[i] = outcome{snap: snap, err: err, dur: time.Since(start)}
		}()
	}
	wg.Wait()

	var res Result
	fetchedAt := now()
	for i, s := range sources {
		o := outs[i]
		st := model.SourceStatus{
			Name:      s.Name(),
			OK:        o.err == nil,
			LatencyMS: float64(o.dur.Microseconds()) / 1000,
			FetchedAt: fetchedAt,
		}
		if o.err != nil {
			st.Error = o.err.Error()
		} else {
			st.Records = o.snap.Records()
			res.Snapshot.Accounts = append(res.Snapshot.Accounts, o.snap.Accounts...)
			res.Snapshot.Tickets = append(res.Snapshot.Tickets, o.snap.Tickets...)
			res.Snapshot.Usage = append(res.Snapshot.Usage, o.snap.Usage...)
		}
		res.Statuses = append(res.Statuses, st)
	}
	res.Warnings = enforceIntegrity(&res.Snapshot)
	return res
}

// fetch runs s.Fetch and returns when it finishes or ctx is done, whichever is
// first. The buffered channel lets a misbehaving source finish later without
// leaking a blocked goroutine.
func fetch(ctx context.Context, s source.Source) (model.Snapshot, error) {
	type result struct {
		snap model.Snapshot
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		defer func() {
			if p := recover(); p != nil {
				ch <- result{err: fmt.Errorf("%s: panic: %v", s.Name(), p)}
			}
		}()
		snap, err := s.Fetch(ctx)
		ch <- result{snap, err}
	}()
	select {
	case r := <-ch:
		return r.snap, r.err
	case <-ctx.Done():
		return model.Snapshot{}, fmt.Errorf("%s: %w", s.Name(), ctx.Err())
	}
}

// enforceIntegrity removes tickets and usage that reference accounts the CRM
// does not know about, and returns one warning per source/account pair.
func enforceIntegrity(s *model.Snapshot) []string {
	known := make(map[string]bool, len(s.Accounts))
	for _, a := range s.Accounts {
		known[a.ID] = true
	}
	orphans := map[string]int{} // "kind:account" -> count

	tickets := s.Tickets[:0]
	for _, t := range s.Tickets {
		if known[t.AccountID] {
			tickets = append(tickets, t)
		} else {
			orphans["ticket(s):"+t.AccountID]++
		}
	}
	s.Tickets = tickets

	usage := s.Usage[:0]
	for _, u := range s.Usage {
		if known[u.AccountID] {
			usage = append(usage, u)
		} else {
			orphans["usage point(s):"+u.AccountID]++
		}
	}
	s.Usage = usage

	var warnings []string
	for k, n := range orphans {
		kind, id, _ := strings.Cut(k, ":")
		warnings = append(warnings, fmt.Sprintf("dropped %d %s for unknown account %q", n, kind, id))
	}
	sort.Strings(warnings)
	return warnings
}
