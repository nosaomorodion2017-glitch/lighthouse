package health

import (
	"reflect"
	"testing"
)

func TestCompute(t *testing.T) {
	const far = 200 // renewal far away, so no renewal signal
	tests := []struct {
		name      string
		in        Input
		wantScore int
		wantLabel string
		wantSigs  int
	}{
		{"perfect", Input{HasUsage: true, DaysToRenewal: far}, 100, "healthy", 0},
		{"growth bonus is capped at 100", Input{HasUsage: true, UsageChangePct: 30, DaysToRenewal: far}, 100, "healthy", 1},
		{"open tickets capped at 30", Input{OpenTickets: 9, HasUsage: true, DaysToRenewal: far}, 70, "watch", 1},
		{"urgent tickets stack with open", Input{OpenTickets: 2, UrgentOpen: 1, HasUsage: true, DaysToRenewal: far}, 73, "watch", 2},
		{"steep usage drop", Input{HasUsage: true, UsageChangePct: -30, DaysToRenewal: far}, 75, "healthy", 1},
		{"mild usage drop", Input{HasUsage: true, UsageChangePct: -12, DaysToRenewal: far}, 90, "healthy", 1},
		{"no usage data", Input{HasUsage: false, DaysToRenewal: far}, 90, "healthy", 1},
		{"everything wrong", Input{OpenTickets: 10, UrgentOpen: 5, HasUsage: true, UsageChangePct: -50, DaysToRenewal: 10}, 15, "at-risk", 4},
		{"renewal signal when not healthy", Input{OpenTickets: 5, HasUsage: true, DaysToRenewal: 20}, 70, "watch", 2},
		{"renewal outside window", Input{OpenTickets: 5, HasUsage: true, DaysToRenewal: 46}, 70, "watch", 1},
		{"no renewal signal when healthy", Input{HasUsage: true, DaysToRenewal: 5}, 100, "healthy", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Compute(tt.in)
			if got.Value != tt.wantScore || got.Label != tt.wantLabel || len(got.Signals) != tt.wantSigs {
				t.Fatalf("Compute(%+v) = %d %s %q; want %d %s with %d signals",
					tt.in, got.Value, got.Label, got.Signals, tt.wantScore, tt.wantLabel, tt.wantSigs)
			}
		})
	}
}

func TestComputeIsDeterministic(t *testing.T) {
	in := Input{OpenTickets: 3, UrgentOpen: 1, HasUsage: true, UsageChangePct: -20, DaysToRenewal: 10}
	if a, b := Compute(in), Compute(in); !reflect.DeepEqual(a, b) {
		t.Fatalf("same input, different output: %+v vs %+v", a, b)
	}
}

func TestLabelBoundaries(t *testing.T) {
	for score, want := range map[int]string{100: "healthy", 75: "healthy", 74: "watch", 50: "watch", 49: "at-risk", 0: "at-risk"} {
		if got := Label(score); got != want {
			t.Errorf("Label(%d) = %s, want %s", score, got, want)
		}
	}
}

func TestChangePct(t *testing.T) {
	tests := []struct {
		series []int
		want   float64
		ok     bool
	}{
		{nil, 0, false},
		{[]int{10}, 0, false},
		{[]int{0, 10}, 0, false},
		{[]int{100, 50}, -50, true},
		{[]int{1, 2, 3, 100, 110, 120, 130, 80}, -20, true}, // compares with 4 weeks earlier (100)
	}
	for _, tt := range tests {
		got, ok := ChangePct(tt.series)
		if got != tt.want || ok != tt.ok {
			t.Errorf("ChangePct(%v) = %v,%v; want %v,%v", tt.series, got, ok, tt.want, tt.ok)
		}
	}
}
