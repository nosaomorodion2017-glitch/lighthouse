// Package health computes an explainable 0–100 account health score.
//
// Every point deducted produces a human-readable signal, so a non-technical
// user can see exactly why an account is flagged. The weights are documented
// in docs/SPEC.md §5 and live only in this file.
package health

import (
	"fmt"
	"math"

	"lighthouse/internal/model"
)

// Input is everything the score depends on. It is a plain value so the score
// is a pure function: same input, same output.
type Input struct {
	OpenTickets    int
	UrgentOpen     int
	UsageChangePct float64
	HasUsage       bool
	DaysToRenewal  int
}

// Score is the result plus the reasons behind it.
type Score struct {
	Value   int
	Label   string
	Signals []string
}

const (
	perOpenTicket    = 6
	maxOpenPenalty   = 30
	perUrgent        = 15
	maxUrgentPenalty = 30
	renewalWindow    = 45
)

// Compute returns the health score for one account.
func Compute(in Input) Score {
	score := 100
	signals := []string{}

	if in.OpenTickets > 0 {
		p := min(in.OpenTickets*perOpenTicket, maxOpenPenalty)
		score -= p
		signals = append(signals, fmt.Sprintf("%d open %s (−%d)", in.OpenTickets, plural(in.OpenTickets, "ticket", "tickets"), p))
	}
	if in.UrgentOpen > 0 {
		p := min(in.UrgentOpen*perUrgent, maxUrgentPenalty)
		score -= p
		signals = append(signals, fmt.Sprintf("%d urgent %s open (−%d)", in.UrgentOpen, plural(in.UrgentOpen, "ticket", "tickets"), p))
	}

	switch {
	case !in.HasUsage:
		score -= 10
		signals = append(signals, "No usage data (−10)")
	case in.UsageChangePct <= -25:
		score -= 25
		signals = append(signals, fmt.Sprintf("Active users down %.0f%% in 4 weeks (−25)", -in.UsageChangePct))
	case in.UsageChangePct <= -10:
		score -= 10
		signals = append(signals, fmt.Sprintf("Active users down %.0f%% in 4 weeks (−10)", -in.UsageChangePct))
	case in.UsageChangePct >= 10:
		score += 5
		signals = append(signals, fmt.Sprintf("Active users up %.0f%% in 4 weeks (+5)", in.UsageChangePct))
	}

	score = max(0, min(100, score))
	label := Label(score)

	if label != model.LabelHealthy && in.DaysToRenewal >= 0 && in.DaysToRenewal <= renewalWindow {
		signals = append(signals, fmt.Sprintf("Renews in %d days", in.DaysToRenewal))
	}
	return Score{Value: score, Label: label, Signals: signals}
}

// Label maps a score to its band.
func Label(score int) string {
	switch {
	case score >= 75:
		return model.LabelHealthy
	case score >= 50:
		return model.LabelWatch
	default:
		return model.LabelAtRisk
	}
}

// ChangePct compares the latest value with the value four weeks earlier (or
// the earliest available). ok is false when there is not enough data.
func ChangePct(series []int) (pct float64, ok bool) {
	if len(series) < 2 {
		return 0, false
	}
	last := series[len(series)-1]
	base := series[max(0, len(series)-5)]
	if base == 0 {
		return 0, false
	}
	p := float64(last-base) / float64(base) * 100
	return math.Round(p*10) / 10, true
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
