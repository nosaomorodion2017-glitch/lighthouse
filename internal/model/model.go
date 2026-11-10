// Package model holds the domain types shared across the platform.
//
// Account is the root entity (owned by the CRM). Tickets and usage points
// reference an account by ID. Views are derived from a Snapshot and never stored.
package model

import "time"

// Account is a customer as recorded in the CRM.
type Account struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Plan        string    `json:"plan"`
	Industry    string    `json:"industry"`
	Owner       string    `json:"owner"`
	MRR         int       `json:"mrr"`
	RenewalDate time.Time `json:"renewal_date"`
}

// Ticket is a support ticket from the help desk.
type Ticket struct {
	ID        string     `json:"id"`
	AccountID string     `json:"account_id"`
	Subject   string     `json:"subject"`
	Priority  string     `json:"priority"` // low | normal | high | urgent
	Status    string     `json:"status"`   // open | pending | closed
	OpenedAt  time.Time  `json:"opened_at"`
	ClosedAt  *time.Time `json:"closed_at,omitempty"`
}

// IsOpen reports whether the ticket still needs work.
func (t Ticket) IsOpen() bool { return t.Status != "closed" }

// UsagePoint is weekly active users for one account in one week.
type UsagePoint struct {
	AccountID   string    `json:"account_id"`
	WeekStart   time.Time `json:"week_start"`
	ActiveUsers int       `json:"active_users"`
}

// Snapshot is everything the sources returned in one collection run.
// Each source fills in only the parts it owns.
type Snapshot struct {
	Accounts []Account
	Tickets  []Ticket
	Usage    []UsagePoint
}

// Records returns the total number of records in the snapshot.
func (s Snapshot) Records() int { return len(s.Accounts) + len(s.Tickets) + len(s.Usage) }

// SourceStatus describes the outcome of fetching one source.
type SourceStatus struct {
	Name      string    `json:"name"`
	OK        bool      `json:"ok"`
	Records   int       `json:"records"`
	LatencyMS float64   `json:"latency_ms"`
	Error     string    `json:"error,omitempty"`
	FetchedAt time.Time `json:"fetched_at"`
}

// Health labels.
const (
	LabelHealthy = "healthy"
	LabelWatch   = "watch"
	LabelAtRisk  = "at-risk"
)

// AccountView is an account joined with its support and usage signals.
type AccountView struct {
	Account
	OpenTickets    int      `json:"open_tickets"`
	UrgentOpen     int      `json:"urgent_open"`
	Usage          []int    `json:"usage"` // weekly active users, oldest first
	UsageChangePct float64  `json:"usage_change_pct"`
	DaysToRenewal  int      `json:"days_to_renewal"`
	Health         int      `json:"health"`
	HealthLabel    string   `json:"health_label"`
	Signals        []string `json:"signals"`
}

// AccountDetail is an AccountView plus the underlying records.
type AccountDetail struct {
	AccountView
	Tickets    []Ticket     `json:"tickets"`
	UsageWeeks []UsagePoint `json:"usage_weeks"`
}

// WeekBucket counts tickets opened and closed in one week (Mon–Sun, UTC).
type WeekBucket struct {
	WeekStart time.Time `json:"week_start"`
	Opened    int       `json:"opened"`
	Closed    int       `json:"closed"`
}

// Summary is the portfolio-level view for the dashboard header.
type Summary struct {
	Accounts    int            `json:"accounts"`
	TotalMRR    int            `json:"total_mrr"`
	MRRAtRisk   int            `json:"mrr_at_risk"`
	OpenTickets int            `json:"open_tickets"`
	UrgentOpen  int            `json:"urgent_open"`
	ByLabel     map[string]int `json:"by_label"`
	Weekly      []WeekBucket   `json:"weekly"`
	RefreshedAt time.Time      `json:"refreshed_at"`
}
