package source

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const header = "ticket_id,account_id,subject,priority,status,opened_at,closed_at\n"

func TestParseTickets(t *testing.T) {
	in := header +
		"T-1,acc-1,Login broken,urgent,open,2026-11-01,\n" +
		"T-2,acc-1,Question,low,closed,2026-10-01,2026-10-02\n"
	got, err := ParseTickets(context.Background(), strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d tickets, want 2", len(got))
	}
	if !got[0].IsOpen() || got[0].ClosedAt != nil {
		t.Errorf("T-1 should be open with no closed_at: %+v", got[0])
	}
	if got[1].IsOpen() || got[1].ClosedAt == nil {
		t.Errorf("T-2 should be closed: %+v", got[1])
	}
}

func TestParseTicketsRejectsBadRows(t *testing.T) {
	tests := map[string]string{
		"bad priority":         "T-1,acc-1,x,critical,open,2026-11-01,\n",
		"bad status":           "T-1,acc-1,x,low,done,2026-11-01,\n",
		"bad date":             "T-1,acc-1,x,low,open,01/11/2026,\n",
		"closed before opened": "T-1,acc-1,x,low,closed,2026-11-05,2026-11-01\n",
		"closed without date":  "T-1,acc-1,x,low,closed,2026-11-05,\n",
		"missing account":      "T-1,,x,low,open,2026-11-05,\n",
		"wrong column count":   "T-1,acc-1,x,low,open\n",
	}
	for name, row := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := ParseTickets(context.Background(), strings.NewReader(header+row))
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), "line 2") {
				t.Errorf("error should name the line: %v", err)
			}
		})
	}
}

func TestParseTicketsRejectsWrongHeader(t *testing.T) {
	_, err := ParseTickets(context.Background(), strings.NewReader("id,account,subject,priority,status,opened,closed\n"))
	if err == nil {
		t.Fatal("expected header error")
	}
}

func TestUsageReshape(t *testing.T) {
	path := writeFile(t, "usage.json", `{"weeks":["2026-10-05","2026-10-12"],"accounts":{"a":[10,12],"b":[5,4]}}`)
	snap, err := UsageFile{Path: path}.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Usage) != 4 {
		t.Fatalf("got %d points, want 4", len(snap.Usage))
	}
}

func TestUsageRejectsRaggedSeries(t *testing.T) {
	path := writeFile(t, "usage.json", `{"weeks":["2026-10-05","2026-10-12"],"accounts":{"a":[10]}}`)
	if _, err := (UsageFile{Path: path}).Fetch(context.Background()); err == nil {
		t.Fatal("expected error for series length mismatch")
	}
}

func TestCRMRejectsDuplicates(t *testing.T) {
	path := writeFile(t, "crm.json", `[{"id":"a","renewal_date":"2027-01-01"},{"id":"a","renewal_date":"2027-01-01"}]`)
	if _, err := (CRMFile{Path: path}).Fetch(context.Background()); err == nil {
		t.Fatal("expected duplicate id error")
	}
}

// The shipped sample data must always load: this catches broken edits to /data.
func TestSampleDataLoads(t *testing.T) {
	dir := filepath.Join("..", "..", "data")
	for _, s := range []Source{
		CRMFile{Path: filepath.Join(dir, "crm.json")},
		SupportCSV{Path: filepath.Join(dir, "support.csv")},
		UsageFile{Path: filepath.Join(dir, "usage.json")},
	} {
		snap, err := s.Fetch(context.Background())
		if err != nil {
			t.Errorf("%s: %v", s.Name(), err)
			continue
		}
		if snap.Records() == 0 {
			t.Errorf("%s: no records", s.Name())
		}
	}
}

func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}
