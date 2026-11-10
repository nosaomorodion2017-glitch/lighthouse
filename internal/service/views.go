package service

import (
	"sort"
	"time"

	"lighthouse/internal/health"
	"lighthouse/internal/model"
)

// weeksInTrend is how many weeks of ticket history the dashboard shows.
const weeksInTrend = 8

// BuildDetails joins a snapshot into one AccountDetail per account. It is a
// pure function of its inputs; all time-dependence goes through now.
func BuildDetails(s model.Snapshot, now time.Time) []model.AccountDetail {
	tickets := map[string][]model.Ticket{}
	for _, t := range s.Tickets {
		tickets[t.AccountID] = append(tickets[t.AccountID], t)
	}
	usage := map[string][]model.UsagePoint{}
	for _, u := range s.Usage {
		usage[u.AccountID] = append(usage[u.AccountID], u)
	}

	today := day(now)
	out := make([]model.AccountDetail, 0, len(s.Accounts))
	for _, a := range s.Accounts {
		ts := tickets[a.ID]
		sortTickets(ts)

		us := usage[a.ID]
		sort.Slice(us, func(i, j int) bool { return us[i].WeekStart.Before(us[j].WeekStart) })
		series := make([]int, len(us))
		for i, u := range us {
			series[i] = u.ActiveUsers
		}

		v := model.AccountView{Account: a, Usage: series}
		for _, t := range ts {
			if t.IsOpen() {
				v.OpenTickets++
				if t.Priority == "urgent" {
					v.UrgentOpen++
				}
			}
		}
		pct, hasUsage := health.ChangePct(series)
		v.UsageChangePct = pct
		v.DaysToRenewal = int(day(a.RenewalDate).Sub(today).Hours() / 24)

		sc := health.Compute(health.Input{
			OpenTickets:    v.OpenTickets,
			UrgentOpen:     v.UrgentOpen,
			UsageChangePct: pct,
			HasUsage:       hasUsage,
			DaysToRenewal:  v.DaysToRenewal,
		})
		v.Health, v.HealthLabel, v.Signals = sc.Value, sc.Label, sc.Signals

		if ts == nil {
			ts = []model.Ticket{}
		}
		if us == nil {
			us = []model.UsagePoint{}
		}
		out = append(out, model.AccountDetail{AccountView: v, Tickets: ts, UsageWeeks: us})
	}

	// Worst health first; ties broken by revenue (biggest first), then name.
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Health != b.Health {
			return a.Health < b.Health
		}
		if a.MRR != b.MRR {
			return a.MRR > b.MRR
		}
		return a.Name < b.Name
	})
	return out
}

// BuildSummary rolls account details up into portfolio numbers.
func BuildSummary(details []model.AccountDetail, now time.Time) model.Summary {
	sum := model.Summary{
		Accounts: len(details),
		ByLabel:  map[string]int{model.LabelHealthy: 0, model.LabelWatch: 0, model.LabelAtRisk: 0},
	}
	thisWeek := WeekStart(now)
	first := thisWeek.AddDate(0, 0, -7*(weeksInTrend-1))
	sum.Weekly = make([]model.WeekBucket, weeksInTrend)
	for i := range sum.Weekly {
		sum.Weekly[i].WeekStart = first.AddDate(0, 0, 7*i)
	}
	bucket := func(t time.Time) int {
		w := WeekStart(t)
		if w.Before(first) || w.After(thisWeek) {
			return -1
		}
		return int(w.Sub(first).Hours() / (24 * 7))
	}

	for _, d := range details {
		sum.TotalMRR += d.MRR
		sum.OpenTickets += d.OpenTickets
		sum.UrgentOpen += d.UrgentOpen
		sum.ByLabel[d.HealthLabel]++
		if d.HealthLabel == model.LabelAtRisk {
			sum.MRRAtRisk += d.MRR
		}
		for _, t := range d.Tickets {
			if i := bucket(t.OpenedAt); i >= 0 {
				sum.Weekly[i].Opened++
			}
			if t.ClosedAt != nil {
				if i := bucket(*t.ClosedAt); i >= 0 {
					sum.Weekly[i].Closed++
				}
			}
		}
	}
	return sum
}

// WeekStart returns 00:00 UTC on the Monday of t's week.
func WeekStart(t time.Time) time.Time {
	t = t.UTC()
	offset := (int(t.Weekday()) + 6) % 7 // Monday = 0
	y, m, d := t.Date()
	return time.Date(y, m, d-offset, 0, 0, 0, 0, time.UTC)
}

func day(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// sortTickets puts open tickets first, then urgent before the rest, newest first.
func sortTickets(ts []model.Ticket) {
	rank := map[string]int{"urgent": 0, "high": 1, "normal": 2, "low": 3}
	sort.SliceStable(ts, func(i, j int) bool {
		a, b := ts[i], ts[j]
		if a.IsOpen() != b.IsOpen() {
			return a.IsOpen()
		}
		if a.IsOpen() && rank[a.Priority] != rank[b.Priority] {
			return rank[a.Priority] < rank[b.Priority]
		}
		return a.OpenedAt.After(b.OpenedAt)
	})
}
