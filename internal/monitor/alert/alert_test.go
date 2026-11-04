package alert

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ACaccel/lcm/internal/signal"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name)) //nolint:gosec // fixture names are literals in this file
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func mustParse(t *testing.T, source, file string, opts Options) []signal.Signal {
	t.Helper()
	sigs, err := Parse(source, fixture(t, file), opts)
	if err != nil {
		t.Fatalf("Parse(%s, %s): %v", source, file, err)
	}
	for _, s := range sigs {
		if s.Kind != signal.KindAlert || s.Source != source {
			t.Errorf("signal kind/source = %s/%s", s.Kind, s.Source)
		}
	}
	return sigs
}

func data(s signal.Signal, key string) string {
	v, _ := s.Data[key].(string)
	return v
}

func TestAlertmanager(t *testing.T) {
	sigs := mustParse(t, SourceAlertmanager, "alertmanager_firing.json", Options{})
	if len(sigs) != 2 {
		t.Fatalf("got %d signals", len(sigs))
	}
	a, b := sigs[0], sigs[1]
	if a.Resource != "api" || data(a, DataAlert) != "HighErrorRate" || data(a, DataStatus) != StatusFiring ||
		a.Severity != signal.SeverityCritical || data(a, DataFingerprint) != "3a5f1c0e7b9d2c41" ||
		data(a, DataMessage) != "Error rate above 5% for 5m" {
		t.Errorf("first alert = %+v", a)
	}
	if a.Labels["env"] != "prod" || a.Labels["job"] != "api" {
		t.Errorf("labels = %v (common labels must merge, alert labels win)", a.Labels)
	}
	// No resource label: unmapped, message falls back to the description.
	if b.Resource != "" || b.Severity != signal.SeverityWarning || data(b, DataMessage) != "/ is 91% full" {
		t.Errorf("second alert = %+v", b)
	}

	res := mustParse(t, SourceAlertmanager, "alertmanager_resolved.json", Options{})
	if res[0].Resource != "api" || data(res[0], DataStatus) != StatusResolved ||
		res[0].Severity != signal.SeverityInfo || data(res[0], DataFingerprint) != data(a, DataFingerprint) {
		t.Errorf("resolved = %+v", res[0])
	}
}

func TestAlertmanagerCustomResourceLabel(t *testing.T) {
	sigs := mustParse(t, SourceAlertmanager, "alertmanager_firing.json", Options{ResourceLabels: []string{"job"}})
	if sigs[0].Resource != "api" || sigs[1].Resource != "node" {
		t.Errorf("resources = %q, %q; want the alert's own job label", sigs[0].Resource, sigs[1].Resource)
	}
}

func TestAlertmanagerFingerprintFallback(t *testing.T) {
	body := []byte(`{"alerts":[{"status":"firing","labels":{"alertname":"A","x":"1"}},{"status":"firing","labels":{"x":"1","alertname":"A"}}]}`)
	sigs, err := Parse(SourceAlertmanager, body, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if data(sigs[0], DataFingerprint) == "" || data(sigs[0], DataFingerprint) != data(sigs[1], DataFingerprint) {
		t.Errorf("fingerprints %q %q", data(sigs[0], DataFingerprint), data(sigs[1], DataFingerprint))
	}
}

func TestUptimeKuma(t *testing.T) {
	down := mustParse(t, SourceUptimeKuma, "uptimekuma_down.json", Options{})
	if len(down) != 1 {
		t.Fatalf("got %d signals", len(down))
	}
	d := down[0]
	if d.Resource != "blog" || data(d, DataAlert) != "Blog" || data(d, DataStatus) != StatusFiring ||
		d.Severity != signal.SeverityCritical || data(d, DataMessage) != "connect ECONNREFUSED 127.0.0.1:8080" {
		t.Errorf("down = %+v", d)
	}
	up := mustParse(t, SourceUptimeKuma, "uptimekuma_up.json", Options{})
	if len(up) != 1 || data(up[0], DataStatus) != StatusResolved || data(up[0], DataFingerprint) != data(d, DataFingerprint) {
		t.Errorf("up = %+v", up)
	}
	if sigs := mustParse(t, SourceUptimeKuma, "uptimekuma_test.json", Options{}); len(sigs) != 0 {
		t.Errorf("test notification produced %d signals", len(sigs))
	}
}

func TestGatus(t *testing.T) {
	sigs := mustParse(t, SourceGatus, "gatus_triggered.json", Options{})
	s := sigs[0]
	if s.Resource != "api" || data(s, DataAlert) != "api-health" || data(s, DataStatus) != StatusFiring ||
		data(s, DataMessage) != "health endpoint down" || s.Labels["group"] != "core" {
		t.Errorf("signal = %+v", s)
	}
	explicit, err := Parse(SourceGatus, []byte(`{"endpoint":"e","status":"TRIGGERED","resource":"router"}`), Options{})
	if err != nil || explicit[0].Resource != "router" {
		t.Errorf("explicit resource: %v %+v", err, explicit)
	}
	resolved, err := Parse(SourceGatus, []byte(`{"endpoint":"api-health","group":"core","status":"RESOLVED"}`), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if data(resolved[0], DataStatus) != StatusResolved || data(resolved[0], DataFingerprint) != data(s, DataFingerprint) {
		t.Errorf("resolved = %+v", resolved[0])
	}
}

func TestGeneric(t *testing.T) {
	sigs := mustParse(t, SourceGeneric, "generic_batch.json", Options{})
	if len(sigs) != 2 {
		t.Fatalf("got %d signals", len(sigs))
	}
	if sigs[0].Resource != "worker" || sigs[0].Severity != signal.SeverityCritical || sigs[0].Labels["queue"] != "mail" {
		t.Errorf("first = %+v", sigs[0])
	}
	if sigs[1].Resource != "proxy" || data(sigs[1], DataStatus) != StatusResolved {
		t.Errorf("second = %+v", sigs[1])
	}
	one, err := Parse(SourceGeneric, []byte(`{"service":"s","alert":"a","status":"firing"}`), Options{})
	if err != nil || len(one) != 1 || one[0].Severity != signal.SeverityWarning || one[0].Resource != "s" {
		t.Errorf("single object: %v %+v", err, one)
	}
	both, err := Parse(SourceGeneric, []byte(`{"resource":"r","service":"s","alert":"a","status":"firing"}`), Options{})
	if err != nil || both[0].Resource != "r" {
		t.Errorf("resource field must win over service: %v %+v", err, both)
	}
	byLabel, err := Parse(SourceGeneric, []byte(`{"alert":"a","status":"firing","labels":{"lcm_resource":"router","service":"x"}}`), Options{})
	if err != nil || byLabel[0].Resource != "router" {
		t.Errorf("lcm_resource label must be preferred: %v %+v", err, byLabel)
	}
}

func TestInvalidPayloads(t *testing.T) {
	cases := []struct{ source, body string }{
		{SourceAlertmanager, `not json`},
		{SourceAlertmanager, `{"status":"firing"}`},
		{SourceAlertmanager, `{"alerts":[{"status":"weird"}]}`},
		{SourceUptimeKuma, `[`},
		{SourceGatus, `{"status":"TRIGGERED"}`},
		{SourceGatus, `{"endpoint":"e","status":"maybe"}`},
		{SourceGeneric, `{"status":"firing"}`},
		{SourceGeneric, `{"alert":"a","status":"on fire"}`},
		{SourceGeneric, `[{"alert":"a","status":"firing"},{"status":"firing"}]`},
	}
	for _, c := range cases {
		if _, err := Parse(c.source, []byte(c.body), Options{}); !errors.Is(err, ErrInvalidPayload) {
			t.Errorf("Parse(%s, %q) error = %v, want ErrInvalidPayload", c.source, c.body, err)
		}
	}
	if _, err := Parse("nagios", nil, Options{}); !errors.Is(err, ErrUnknownSource) {
		t.Errorf("unknown source error = %v", err)
	}
}
