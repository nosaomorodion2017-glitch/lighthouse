// Package source contains the adapters that read from upstream systems.
//
// Each adapter returns a partial model.Snapshot containing only the records it
// owns. Adding a new upstream system means adding one type that implements
// Source; nothing else in the platform needs to change.
package source

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"lighthouse/internal/model"
)

// Source is one upstream system.
type Source interface {
	Name() string
	Fetch(ctx context.Context) (model.Snapshot, error)
}

const dateLayout = "2006-01-02"

// ---------------------------------------------------------------------------
// CRM: JSON array of accounts.

// CRMFile reads accounts from a JSON file exported from the CRM.
type CRMFile struct{ Path string }

func (CRMFile) Name() string { return "crm" }

type crmRecord struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Plan        string `json:"plan"`
	Industry    string `json:"industry"`
	Owner       string `json:"owner"`
	MRR         int    `json:"mrr"`
	RenewalDate string `json:"renewal_date"`
}

func (s CRMFile) Fetch(ctx context.Context) (model.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return model.Snapshot{}, err
	}
	var recs []crmRecord
	if err := readJSON(s.Path, &recs); err != nil {
		return model.Snapshot{}, fmt.Errorf("crm: %w", err)
	}
	seen := make(map[string]bool, len(recs))
	accounts := make([]model.Account, 0, len(recs))
	for i, r := range recs {
		if r.ID == "" {
			return model.Snapshot{}, fmt.Errorf("crm: record %d: missing id", i)
		}
		if seen[r.ID] {
			return model.Snapshot{}, fmt.Errorf("crm: record %d: duplicate id %q", i, r.ID)
		}
		seen[r.ID] = true
		if r.MRR < 0 {
			return model.Snapshot{}, fmt.Errorf("crm: account %s: negative mrr", r.ID)
		}
		renewal, err := time.Parse(dateLayout, r.RenewalDate)
		if err != nil {
			return model.Snapshot{}, fmt.Errorf("crm: account %s: renewal_date: %w", r.ID, err)
		}
		accounts = append(accounts, model.Account{
			ID: r.ID, Name: r.Name, Plan: r.Plan, Industry: r.Industry,
			Owner: r.Owner, MRR: r.MRR, RenewalDate: renewal,
		})
	}
	return model.Snapshot{Accounts: accounts}, nil
}

// ---------------------------------------------------------------------------
// Support desk: CSV export of tickets.

// SupportCSV reads tickets from a CSV export of the help desk.
type SupportCSV struct{ Path string }

func (SupportCSV) Name() string { return "support" }

var (
	validPriority = map[string]bool{"low": true, "normal": true, "high": true, "urgent": true}
	validStatus   = map[string]bool{"open": true, "pending": true, "closed": true}
	ticketHeader  = []string{"ticket_id", "account_id", "subject", "priority", "status", "opened_at", "closed_at"}
)

func (s SupportCSV) Fetch(ctx context.Context) (model.Snapshot, error) {
	f, err := os.Open(s.Path)
	if err != nil {
		return model.Snapshot{}, fmt.Errorf("support: %w", err)
	}
	defer f.Close()
	tickets, err := ParseTickets(ctx, f)
	if err != nil {
		return model.Snapshot{}, fmt.Errorf("support: %w", err)
	}
	return model.Snapshot{Tickets: tickets}, nil
}

// ParseTickets parses the help-desk CSV format. It is strict: any malformed
// row fails the whole parse with the line number, because silently dropping
// tickets would make health scores look better than they are.
func ParseTickets(ctx context.Context, r io.Reader) ([]model.Ticket, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = len(ticketHeader)
	header, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}
	for i, want := range ticketHeader {
		if strings.TrimSpace(header[i]) != want {
			return nil, fmt.Errorf("header column %d: got %q, want %q", i+1, header[i], want)
		}
	}
	var out []model.Ticket
	for line := 2; ; line++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		row, err := cr.Read()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		t, err := parseTicketRow(row)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		out = append(out, t)
	}
}

func parseTicketRow(row []string) (model.Ticket, error) {
	for i := range row {
		row[i] = strings.TrimSpace(row[i])
	}
	t := model.Ticket{ID: row[0], AccountID: row[1], Subject: row[2], Priority: row[3], Status: row[4]}
	if t.ID == "" || t.AccountID == "" {
		return t, errors.New("ticket_id and account_id are required")
	}
	if !validPriority[t.Priority] {
		return t, fmt.Errorf("invalid priority %q", t.Priority)
	}
	if !validStatus[t.Status] {
		return t, fmt.Errorf("invalid status %q", t.Status)
	}
	opened, err := time.Parse(dateLayout, row[5])
	if err != nil {
		return t, fmt.Errorf("opened_at: %w", err)
	}
	t.OpenedAt = opened
	if row[6] != "" {
		closed, err := time.Parse(dateLayout, row[6])
		if err != nil {
			return t, fmt.Errorf("closed_at: %w", err)
		}
		if closed.Before(opened) {
			return t, errors.New("closed_at is before opened_at")
		}
		t.ClosedAt = &closed
	}
	if t.Status == "closed" && t.ClosedAt == nil {
		return t, errors.New("closed ticket has no closed_at")
	}
	return t, nil
}

// ---------------------------------------------------------------------------
// Product analytics: weekly active users, stored column-wise.

// UsageFile reads weekly active users from the analytics export. The export is
// column-oriented ({weeks: [...], accounts: {id: [...]}}) so it is reshaped
// into one UsagePoint per account per week.
type UsageFile struct{ Path string }

func (UsageFile) Name() string { return "usage" }

type usageExport struct {
	Weeks    []string         `json:"weeks"`
	Accounts map[string][]int `json:"accounts"`
}

func (s UsageFile) Fetch(ctx context.Context) (model.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return model.Snapshot{}, err
	}
	var exp usageExport
	if err := readJSON(s.Path, &exp); err != nil {
		return model.Snapshot{}, fmt.Errorf("usage: %w", err)
	}
	points, err := exp.points()
	if err != nil {
		return model.Snapshot{}, fmt.Errorf("usage: %w", err)
	}
	return model.Snapshot{Usage: points}, nil
}

func (e usageExport) points() ([]model.UsagePoint, error) {
	weeks := make([]time.Time, len(e.Weeks))
	for i, w := range e.Weeks {
		t, err := time.Parse(dateLayout, w)
		if err != nil {
			return nil, fmt.Errorf("week %d: %w", i, err)
		}
		weeks[i] = t
	}
	var out []model.UsagePoint
	for id, series := range e.Accounts {
		if len(series) != len(weeks) {
			return nil, fmt.Errorf("account %s: %d values for %d weeks", id, len(series), len(weeks))
		}
		for i, v := range series {
			out = append(out, model.UsagePoint{AccountID: id, WeekStart: weeks[i], ActiveUsers: v})
		}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Static is an in-memory source, used in tests and demos.

type Static struct {
	SourceName string
	Snapshot   model.Snapshot
	Err        error
	Delay      time.Duration
}

func (s Static) Name() string { return s.SourceName }

func (s Static) Fetch(ctx context.Context) (model.Snapshot, error) {
	if s.Delay > 0 {
		select {
		case <-time.After(s.Delay):
		case <-ctx.Done():
			return model.Snapshot{}, ctx.Err()
		}
	}
	if s.Err != nil {
		return model.Snapshot{}, s.Err
	}
	return s.Snapshot, nil
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}
