package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"lighthouse/internal/insight"
	"lighthouse/internal/model"
	"lighthouse/internal/obs"
	"lighthouse/internal/service"
	"lighthouse/internal/source"
)

type countingBriefer struct{ calls atomic.Int32 }

func (c *countingBriefer) Brief(_ context.Context, d model.AccountDetail) (insight.Brief, error) {
	c.calls.Add(1)
	return insight.Brief{Text: "brief for " + d.Name, Source: "fake"}, nil
}

func newTestServer(t *testing.T, sources ...source.Source) (*httptest.Server, *countingBriefer) {
	t.Helper()
	if len(sources) == 0 {
		sources = []source.Source{source.Static{SourceName: "crm", Snapshot: model.Snapshot{
			Accounts: []model.Account{
				{ID: "a", Name: "Alpha", MRR: 100, RenewalDate: time.Now().AddDate(1, 0, 0)},
				{ID: "b", Name: "Beta", MRR: 200, RenewalDate: time.Now().AddDate(1, 0, 0)},
			},
		}}}
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := service.New(service.Config{Sources: sources, Logger: log})
	_ = svc.Refresh(context.Background())
	b := &countingBriefer{}
	static := fstest.MapFS{"index.html": {Data: []byte("<h1>dash</h1>")}}
	srv := httptest.NewServer(New(svc, b, obs.New(), log, static).Handler())
	t.Cleanup(srv.Close)
	return srv, b
}

func get(t *testing.T, url string) (*http.Response, map[string]any) {
	t.Helper()
	return do(t, http.MethodGet, url)
}

func do(t *testing.T, method, url string) (*http.Response, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(method, url, nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var body map[string]any
	if strings.HasPrefix(res.Header.Get("Content-Type"), "application/json") {
		if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
	}
	return res, body
}

func TestEndpoints(t *testing.T) {
	srv, _ := newTestServer(t)
	tests := []struct {
		method, path string
		want         int
	}{
		{"GET", "/healthz", 200},
		{"GET", "/readyz", 200},
		{"GET", "/api/summary", 200},
		{"GET", "/api/accounts", 200},
		{"GET", "/api/accounts?label=at-risk&sort=mrr&q=al", 200},
		{"GET", "/api/accounts?label=bogus", 400},
		{"GET", "/api/accounts?sort=bogus", 400},
		{"GET", "/api/accounts/a", 200},
		{"GET", "/api/accounts/missing", 404},
		{"POST", "/api/accounts/missing/brief", 404},
		{"POST", "/api/accounts", 405},
		{"GET", "/api/sources", 200},
		{"POST", "/api/refresh", 200},
		{"GET", "/", 200},
	}
	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			res, _ := do(t, tt.method, srv.URL+tt.path)
			if res.StatusCode != tt.want {
				t.Fatalf("status = %d, want %d", res.StatusCode, tt.want)
			}
		})
	}
}

func TestErrorShape(t *testing.T) {
	srv, _ := newTestServer(t)
	_, body := get(t, srv.URL+"/api/accounts?label=bogus")
	e, ok := body["error"].(map[string]any)
	if !ok || e["code"] != "invalid_label" || e["message"] == "" {
		t.Fatalf("unexpected error body: %v", body)
	}
}

func TestBriefIsCachedPerDataVersion(t *testing.T) {
	srv, b := newTestServer(t)
	for range 3 {
		res, body := do(t, "POST", srv.URL+"/api/accounts/a/brief")
		if res.StatusCode != 200 || body["text"] != "brief for Alpha" {
			t.Fatalf("status %d body %v", res.StatusCode, body)
		}
	}
	if n := b.calls.Load(); n != 1 {
		t.Fatalf("briefer called %d times, want 1 (cached)", n)
	}
	do(t, "POST", srv.URL+"/api/refresh") // new data version invalidates cache
	do(t, "POST", srv.URL+"/api/accounts/a/brief")
	if n := b.calls.Load(); n != 2 {
		t.Fatalf("briefer called %d times after refresh, want 2", n)
	}
}

func TestReadyzBeforeAnyData(t *testing.T) {
	srv, _ := newTestServer(t, source.Static{SourceName: "crm", Err: errors.New("down")})
	res, _ := get(t, srv.URL+"/readyz")
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("readyz = %d, want 503", res.StatusCode)
	}
	res, _ = do(t, "POST", srv.URL+"/api/refresh")
	if res.StatusCode != http.StatusBadGateway {
		t.Fatalf("refresh = %d, want 502", res.StatusCode)
	}
}

func TestObservabilityHeadersAndMetrics(t *testing.T) {
	srv, _ := newTestServer(t)
	req, _ := http.NewRequest("GET", srv.URL+"/api/accounts", nil)
	req.Header.Set("X-Request-ID", "abc123")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.Header.Get("X-Request-ID") != "abc123" {
		t.Error("request ID should be echoed")
	}
	if res.Header.Get("Content-Security-Policy") == "" || res.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Error("security headers missing")
	}

	res, err = http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if want := `http_requests_total{route="GET /api/accounts",code="200"} 1`; !strings.Contains(string(b), want) {
		t.Fatalf("metrics missing %q:\n%s", want, b)
	}
}
