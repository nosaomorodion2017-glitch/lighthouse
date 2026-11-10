// Package insight generates short account briefs for account owners.
//
// The LLM only ever sees facts the platform already holds (a compact JSON of
// one account), and is told not to invent anything. If the LLM is not
// configured or fails, a deterministic rule-based brief is returned instead,
// so the feature never breaks the workflow it sits in.
package insight

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"lighthouse/internal/model"
)

// Brief is what the user sees.
type Brief struct {
	Text   string `json:"text"`
	Source string `json:"source"` // "claude" or "rules"
	Model  string `json:"model,omitempty"`
	Note   string `json:"note,omitempty"`
}

// Briefer writes a brief for one account.
type Briefer interface {
	Brief(ctx context.Context, d model.AccountDetail) (Brief, error)
}

// ---------------------------------------------------------------------------
// Facts: the only data the LLM is allowed to see.

type facts struct {
	Account        string   `json:"account"`
	Plan           string   `json:"plan"`
	Industry       string   `json:"industry"`
	MRR            int      `json:"mrr_usd"`
	DaysToRenewal  int      `json:"days_to_renewal"`
	Health         int      `json:"health_score"`
	HealthLabel    string   `json:"health_label"`
	Signals        []string `json:"signals"`
	WeeklyActive   []int    `json:"weekly_active_users_oldest_first"`
	OpenTickets    []string `json:"open_tickets"`
	RecentlyClosed []string `json:"recently_closed_tickets"`
}

func factsFor(d model.AccountDetail) facts {
	f := facts{
		Account: d.Name, Plan: d.Plan, Industry: d.Industry, MRR: d.MRR,
		DaysToRenewal: d.DaysToRenewal, Health: d.Health, HealthLabel: d.HealthLabel,
		Signals: d.Signals, WeeklyActive: d.Usage,
		OpenTickets: []string{}, RecentlyClosed: []string{},
	}
	for _, t := range d.Tickets {
		line := fmt.Sprintf("[%s] %s (opened %s)", t.Priority, t.Subject, t.OpenedAt.Format("2006-01-02"))
		if t.IsOpen() {
			f.OpenTickets = append(f.OpenTickets, line)
		} else if len(f.RecentlyClosed) < 3 {
			f.RecentlyClosed = append(f.RecentlyClosed, line)
		}
	}
	return f
}

// ---------------------------------------------------------------------------
// Claude.

const systemPrompt = `You write account briefs for a customer-success team.
Use ONLY the facts in the user's JSON. Never invent numbers, names, dates or events.
If something is unknown, say so.
Format exactly:
- three bullet points (each under 25 words) on the account's situation, most important first
- then one line starting "Next action:" with a single concrete step for the account owner.
No preamble, no headings.`

// Claude calls the Anthropic Messages API.
type Claude struct {
	APIKey  string
	Model   string
	BaseURL string // default https://api.anthropic.com
	Client  *http.Client
}

func (c Claude) Brief(ctx context.Context, d model.AccountDetail) (Brief, error) {
	if c.APIKey == "" {
		return Brief{}, errors.New("claude: no API key configured")
	}
	base := c.BaseURL
	if base == "" {
		base = "https://api.anthropic.com"
	}
	client := c.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}

	fj, err := json.Marshal(factsFor(d))
	if err != nil {
		return Brief{}, err
	}
	reqBody, err := json.Marshal(map[string]any{
		"model":      c.Model,
		"max_tokens": 400,
		"system":     systemPrompt,
		"messages": []map[string]string{{
			"role":    "user",
			"content": "Account facts:\n" + string(fj),
		}},
	})
	if err != nil {
		return Brief{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/messages", bytes.NewReader(reqBody))
	if err != nil {
		return Brief{}, err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("x-api-key", c.APIKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := client.Do(req)
	if err != nil {
		return Brief{}, fmt.Errorf("claude: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Brief{}, fmt.Errorf("claude: read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return Brief{}, fmt.Errorf("claude: status %d: %s", resp.StatusCode, truncate(string(body), 300))
	}

	var out struct {
		Model   string `json:"model"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return Brief{}, fmt.Errorf("claude: decode: %w", err)
	}
	var sb strings.Builder
	for _, b := range out.Content {
		if b.Type == "text" {
			sb.WriteString(b.Text)
		}
	}
	text := strings.TrimSpace(sb.String())
	if text == "" {
		return Brief{}, errors.New("claude: empty response")
	}
	return Brief{Text: text, Source: "claude", Model: out.Model}, nil
}

// ---------------------------------------------------------------------------
// Rules: deterministic fallback.

// Rules writes a brief from the health signals without any external call.
type Rules struct{}

func (Rules) Brief(_ context.Context, d model.AccountDetail) (Brief, error) {
	var lines []string
	lines = append(lines, fmt.Sprintf("- Health %d/100 (%s), %s MRR on the %s plan.", d.Health, d.HealthLabel, dollars(d.MRR), d.Plan))
	if len(d.Signals) == 0 {
		lines = append(lines, "- No negative signals: no open tickets and usage is stable.")
	} else {
		lines = append(lines, "- Signals: "+strings.Join(d.Signals, "; ")+".")
	}
	for _, t := range d.Tickets {
		if t.IsOpen() {
			lines = append(lines, fmt.Sprintf("- Top open ticket: [%s] %s.", t.Priority, t.Subject))
			break
		}
	}

	var action string
	switch {
	case d.UrgentOpen > 0:
		action = "Escalate the urgent ticket with support today and update the customer."
	case d.HealthLabel != model.LabelHealthy && d.DaysToRenewal >= 0 && d.DaysToRenewal <= 45:
		action = fmt.Sprintf("Book a renewal call this week — renewal is in %d days.", d.DaysToRenewal)
	case d.UsageChangePct <= -10:
		action = "Ask the customer's champion what changed and offer a training session."
	case d.HealthLabel == model.LabelHealthy && d.UsageChangePct >= 10:
		action = "Usage is growing — explore an expansion conversation."
	default:
		action = "No action needed; review again next week."
	}
	lines = append(lines, "Next action: "+action)
	return Brief{Text: strings.Join(lines, "\n"), Source: "rules"}, nil
}

// ---------------------------------------------------------------------------
// Fallback.

// Fallback tries Primary and falls back to Secondary on any error.
type Fallback struct {
	Primary   Briefer // may be nil when no LLM is configured
	Secondary Briefer
	Logger    *slog.Logger
}

func (f Fallback) Brief(ctx context.Context, d model.AccountDetail) (Brief, error) {
	if f.Primary != nil {
		b, err := f.Primary.Brief(ctx, d)
		if err == nil {
			return b, nil
		}
		if f.Logger != nil {
			f.Logger.Warn("primary briefer failed, using fallback", "account", d.ID, "error", err)
		}
		b, err2 := f.Secondary.Brief(ctx, d)
		b.Note = "AI provider unavailable — showing the rule-based brief."
		return b, err2
	}
	b, err := f.Secondary.Brief(ctx, d)
	b.Note = "No AI provider configured — showing the rule-based brief. Set ANTHROPIC_API_KEY to enable Claude."
	return b, err
}

// dollars formats 18400 as "$18,400".
func dollars(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return "$" + s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
