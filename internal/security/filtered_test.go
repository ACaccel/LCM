package security

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/model"
)

func queryService(t *testing.T, f *fixture) {
	t.Helper()
	if err := f.cfg.DB.Write(f.ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(f.ctx, `INSERT INTO service_versions(name,version,document,created_at,created_by_kind,created_by_id,created_by_name) VALUES('website',1,'{}',0,'system','test','test')`); err != nil {
			return err
		}
		_, err := tx.ExecContext(f.ctx, `INSERT INTO services(name,host,version,state,state_since,created_at,updated_at) VALUES('website','local',1,'running',0,0,0)`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestFilteredIncidentQueriesAndStableCursor(t *testing.T) {
	f := setup(t)
	queryService(t, f)
	first := f.ingest(t, f.event("first-query")).IncidentID
	start := f.clk.Now()
	f.clk.Advance(time.Minute)
	e := f.event("service-query")
	e.Fingerprint, e.Service, e.Source, e.Severity = "different", "website", "aide", "critical"
	second := f.ingest(t, e).IncidentID
	f.clk.Advance(time.Minute)
	e = f.event("third-query")
	e.Fingerprint = "third"
	third := f.ingest(t, e).IncidentID
	rows, err := f.m.ListFiltered(f.ctx, IncidentFilter{}, 0, 1)
	if err != nil || len(rows) != 1 || rows[0].ID != third {
		t.Fatalf("first page: %+v %v", rows, err)
	}
	rows, err = f.m.ListFiltered(f.ctx, IncidentFilter{}, rows[0].Seq, 1)
	if err != nil || len(rows) != 1 || rows[0].ID != second {
		t.Fatalf("cursor page: %+v %v", rows, err)
	}
	for name, test := range map[string]struct {
		filter IncidentFilter
		want   []string
	}{
		"combined":          {IncidentFilter{MachineID: "machine-a", Service: "website", Source: "aide", Severity: "critical", State: "firing", From: start.Add(time.Minute), To: start.Add(2 * time.Minute)}, []string{second}},
		"exclusive end":     {IncidentFilter{From: start, To: start.Add(time.Minute)}, []string{first}},
		"unassigned":        {IncidentFilter{Service: "__unassigned__"}, []string{third, first}},
		"unknown machine":   {IncidentFilter{MachineID: "missing"}, nil},
		"SQL literal":       {IncidentFilter{MachineID: "machine-a' OR 1=1--"}, nil},
		"mismatched source": {IncidentFilter{Source: "auditd"}, nil},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := f.m.ListFiltered(f.ctx, test.filter, 0, 50)
			if err != nil || len(got) != len(test.want) {
				t.Fatalf("filtered rows: %+v %v", got, err)
			}
			for i, id := range test.want {
				if got[i].ID != id {
					t.Fatalf("row %d: got %s, want %s", i, got[i].ID, id)
				}
			}
		})
	}
	got, err := f.m.IncidentByID(f.ctx, second)
	if err != nil || got.ID != second || got.Service != "website" {
		t.Fatalf("detail: %+v %v", got, err)
	}
	if _, err = f.m.IncidentByID(f.ctx, "not-found"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing detail: %v", err)
	}
}

func TestFilteredTrafficQueriesCompleteWindowsAndCursor(t *testing.T) {
	f := setup(t)
	queryService(t, f)
	end := f.clk.Now()
	for _, traffic := range []Traffic{
		{MachineID: "machine-a", Category: "auth", WindowStart: end.Add(-3 * time.Minute), WindowEnd: end.Add(-2 * time.Minute), Requests: 10},
		{MachineID: "machine-a", Service: "website", Category: "api", WindowStart: end.Add(-2 * time.Minute), WindowEnd: end.Add(-time.Minute), Requests: 20},
		{MachineID: "machine-a", Category: "auth", WindowStart: end.Add(-time.Minute), WindowEnd: end, Requests: 30},
	} {
		if _, err := f.m.IngestTraffic(f.ctx, traffic); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := f.m.TrafficFiltered(f.ctx, TrafficFilter{}, 0, 1)
	if err != nil || len(rows) != 1 || rows[0].Requests != 30 {
		t.Fatalf("first page: %+v %v", rows, err)
	}
	rows, err = f.m.TrafficFiltered(f.ctx, TrafficFilter{}, rows[0].ID, 1)
	if err != nil || len(rows) != 1 || rows[0].Requests != 20 {
		t.Fatalf("second page: %+v %v", rows, err)
	}
	for name, test := range map[string]struct {
		filter TrafficFilter
		want   int64
	}{
		"combined":               {TrafficFilter{MachineID: "machine-a", Service: "website", Category: "api", From: end.Add(-2 * time.Minute), To: end.Add(-time.Minute)}, 20},
		"partial start excluded": {TrafficFilter{From: end.Add(-90 * time.Second)}, 30},
		"partial end excluded":   {TrafficFilter{From: end.Add(-3 * time.Minute), To: end.Add(-90 * time.Second)}, 10},
		"unassigned":             {TrafficFilter{Service: "__unassigned__", From: end.Add(-time.Minute)}, 30},
		"literal machine":        {TrafficFilter{MachineID: "machine-a' OR 1=1--"}, 0},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := f.m.TrafficFiltered(f.ctx, test.filter, 0, 50)
			if err != nil {
				t.Fatal(err)
			}
			if test.want == 0 && len(got) == 0 {
				return
			}
			if len(got) != 1 || got[0].Requests != test.want {
				t.Fatalf("filtered rows: %+v", got)
			}
		})
	}
}

func TestFilteredQueriesRejectInvalidInput(t *testing.T) {
	f := setup(t)
	now := f.clk.Now()
	for name, filter := range map[string]IncidentFilter{
		"machine length":  {MachineID: strings.Repeat("x", 101)},
		"machine control": {MachineID: "machine\n"},
		"service":         {Service: "../../etc"},
		"source":          {Source: "raw"},
		"severity":        {Severity: "high"},
		"state":           {State: "acknowledged"},
		"order":           {From: now, To: now},
		"timestamp":       {From: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := f.m.ListFiltered(f.ctx, filter, 0, 5)
			var issues model.Issues
			if !errors.As(err, &issues) {
				t.Fatalf("wanted model.Issues, got %v", err)
			}
		})
	}
	for _, query := range []struct {
		before int64
		limit  int
	}{{-1, 10}, {0, 0}, {0, 502}} {
		if _, err := f.m.ListFiltered(f.ctx, IncidentFilter{}, query.before, query.limit); err == nil {
			t.Fatalf("accepted incident page %+v", query)
		}
		if _, err := f.m.TrafficFiltered(f.ctx, TrafficFilter{}, query.before, query.limit); err == nil {
			t.Fatalf("accepted traffic page %+v", query)
		}
	}
	for _, filter := range []TrafficFilter{{Category: "raw"}, {Service: "UPPER"}, {From: now, To: now.Add(-time.Second)}} {
		if _, err := f.m.TrafficFiltered(f.ctx, filter, 0, 5); err == nil {
			t.Fatalf("accepted traffic filter %+v", filter)
		}
	}
	if _, err := f.m.IncidentByID(f.ctx, ""); err == nil {
		t.Fatal("accepted empty incident id")
	}
}
