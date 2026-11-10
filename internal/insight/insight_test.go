package insight

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"lighthouse/internal/model"
)

func detail() model.AccountDetail {
	return model.AccountDetail{
		AccountView: model.AccountView{
			Account:     model.Account{ID: "a", Name: "Alpha", Plan: "Growth", MRR: 4000},
			OpenTickets: 1, UrgentOpen: 1, Health: 55, HealthLabel: model.LabelWatch,
			Signals: []string{"1 urgent ticket open (−15)"}, DaysToRenewal: 20,
		},
		Tickets: []model.Ticket{{ID: "t", Subject: "Import failing", Priority: "urgent", Status: "open"}},
	}
}

func TestClaudeSendsFactsAndParsesReply(t *testing.T) {
	var got struct {
		Model    string `json:"model"`
		System   string `json:"system"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" || r.Method != http.MethodPost {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("x-api-key") != "test-key" || r.Header.Get("anthropic-version") == "" {
			t.Errorf("missing auth/version headers")
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"model":"claude-test","content":[{"type":"text","text":"- one\n- two\n- three\nNext action: call them"}]}`))
	}))
	defer srv.Close()

	b, err := Claude{APIKey: "test-key", Model: "claude-test", BaseURL: srv.URL}.Brief(context.Background(), detail())
	if err != nil {
		t.Fatal(err)
	}
	if b.Source != "claude" || b.Model != "claude-test" || !strings.Contains(b.Text, "Next action") {
		t.Fatalf("unexpected brief: %+v", b)
	}
	if got.Model != "claude-test" || !strings.Contains(got.System, "ONLY the facts") {
		t.Errorf("request model/system wrong: %q", got.Model)
	}
	if len(got.Messages) != 1 || !strings.Contains(got.Messages[0].Content, "Import failing") {
		t.Errorf("facts not sent to the model: %+v", got.Messages)
	}
}

func TestClaudeErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"overloaded"}`, 529)
	}))
	defer srv.Close()
	_, err := Claude{APIKey: "k", Model: "m", BaseURL: srv.URL}.Brief(context.Background(), detail())
	if err == nil || !strings.Contains(err.Error(), "529") {
		t.Fatalf("want status error, got %v", err)
	}
}

func TestClaudeWithoutKeyFailsFast(t *testing.T) {
	if _, err := (Claude{}).Brief(context.Background(), detail()); err == nil {
		t.Fatal("expected error without API key")
	}
}

type failing struct{}

func (failing) Brief(context.Context, model.AccountDetail) (Brief, error) {
	return Brief{}, errors.New("provider down")
}

func TestFallbackUsesRulesOnError(t *testing.T) {
	b, err := Fallback{Primary: failing{}, Secondary: Rules{}}.Brief(context.Background(), detail())
	if err != nil {
		t.Fatal(err)
	}
	if b.Source != "rules" || b.Note == "" {
		t.Fatalf("expected rules brief with note, got %+v", b)
	}
}

func TestFallbackWithoutPrimary(t *testing.T) {
	b, _ := Fallback{Secondary: Rules{}}.Brief(context.Background(), detail())
	if b.Source != "rules" || !strings.Contains(b.Note, "ANTHROPIC_API_KEY") {
		t.Fatalf("unexpected: %+v", b)
	}
}

func TestDollars(t *testing.T) {
	for in, want := range map[int]string{0: "$0", 900: "$900", 1000: "$1,000", 18400: "$18,400", 1234567: "$1,234,567"} {
		if got := dollars(in); got != want {
			t.Errorf("dollars(%d) = %s, want %s", in, got, want)
		}
	}
}

func TestRulesPrioritisesUrgentTickets(t *testing.T) {
	b, _ := Rules{}.Brief(context.Background(), detail())
	if !strings.Contains(b.Text, "Next action: Escalate the urgent ticket") {
		t.Fatalf("urgent ticket should drive the next action:\n%s", b.Text)
	}
	if !strings.Contains(b.Text, "Import failing") {
		t.Errorf("top open ticket should be mentioned:\n%s", b.Text)
	}
}
