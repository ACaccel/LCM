package app_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/clock"
	"github.com/ACaccel/lcm/internal/machine"
	"github.com/ACaccel/lcm/internal/maintenance"
	"github.com/ACaccel/lcm/internal/model"
)

type machineProbe struct {
	n uint64
	// device and boot replace the synthetic identity when set.
	device, boot string
}

func (p *machineProbe) Observe(context.Context, model.MachineConfig) (machine.Snapshot, error) {
	p.n += 100
	s := machine.Snapshot{DeviceHash: "synthetic-device", BootID: "boot-a", Init: "systemd", CPUTotal: p.n, CPUIdle: p.n * 9 / 10, CPUCount: 4, MemoryTotal: 100, MemoryAvailable: 90, Filesystems: []machine.Filesystem{{Path: "/", Total: 100, Available: 90}}, Coverage: map[string]machine.Coverage{}}
	for _, key := range []string{"identity", "boot", "init", "uptime", "cpu", "memory"} {
		s.Coverage[key] = machine.Coverage{State: "available"}
	}
	if p.device != "" {
		s.DeviceHash = p.device
	}
	if p.boot != "" {
		s.BootID = p.boot
	}
	return s, nil
}
func (*machineProbe) ReadJournal(context.Context, model.MachineConfig, string, time.Time) (machine.JournalBatch, error) {
	return machine.JournalBatch{}, nil
}

type machinePowerFake struct {
	submits      int
	receipt      machine.Receipt
	clk          *clock.Fake
	preflightErr error
}

func (p *machinePowerFake) Preflight(context.Context) error { return p.preflightErr }
func (*machinePowerFake) Prepare(_ context.Context, r machine.PowerRequest) (machine.Receipt, error) {
	return machine.Receipt{State: "prepared", BootID: r.BootID}, nil
}
func (p *machinePowerFake) Submit(_ context.Context, r machine.PowerRequest) (machine.Receipt, error) {
	p.submits++
	p.receipt = machine.Receipt{State: "accepted", BootID: r.BootID, AcceptedAt: p.clk.Now()}
	return p.receipt, nil
}
func (p *machinePowerFake) Query(context.Context, machine.PowerRequest) (machine.Receipt, error) {
	return p.receipt, nil
}

func TestMachineHTTPAndMaintenanceIntegration(t *testing.T) {
	clk := clock.NewFake(epoch)
	power := &machinePowerFake{clk: clk}
	h := newHarness(t, func(o *testOpts) { o.clk = clk; o.machineObserver = &machineProbe{}; o.machinePower = power })
	_, admin, err := h.app.Auth.CreateToken(h.ctx, "machine-admin", auth.ScopeAdmin)
	if err != nil {
		t.Fatal(err)
	}
	_, reader, err := h.app.Auth.CreateToken(h.ctx, "machine-ingest", auth.ScopeIngest)
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path, body, token, version string, want int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		if version != "" {
			r.Header.Set("If-Match", version)
		}
		w := httptest.NewRecorder()
		h.app.Handler().ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: got %d want %d: %s", method, path, w.Code, want, w.Body.String())
		}
		return w
	}
	call("GET", "/resources?type=machine", "", "", "", 401)
	call("GET", "/resources?type=machine", "", reader, "", 403)
	doc := `{"apiVersion":"lcm/v2","kind":"Resource","metadata":{"name":"workstation"},"spec":{"type":"machine","adapter":"local"}}`
	call("POST", "/resources", doc, admin, "", 201)
	call("POST", "/resources/workstation/phase", `{"phase":"retired"}`, admin, "", 422)
	call("POST", "/resources/workstation/phase", `{"phase":"retired"}`, admin, "2", 409)
	// A retired machine is not observed; resuming brings it back.
	call("POST", "/resources/workstation/phase", `{"phase":"retired","version":1,"reason":"test"}`, admin, "", 200)
	call("POST", "/resources/workstation/observe", "", admin, "", 409)
	call("POST", "/resources/workstation/phase", `{"phase":"managed"}`, admin, "1", 200)
	for range 2 {
		clk.Advance(30 * time.Second)
		call("POST", "/resources/workstation/observe", "", admin, "", 200)
	}
	w := call("GET", "/resources/workstation/history", "", admin, "", 200)
	var hist struct {
		Items []struct {
			Type, ResourceID string
			Data             *machine.Snapshot
		}
	}
	if err = json.Unmarshal(w.Body.Bytes(), &hist); err != nil || len(hist.Items) != 2 || hist.Items[0].Type != "machine" ||
		hist.Items[0].Data == nil || hist.Items[0].ResourceID == "" {
		t.Fatalf("history = %s (%v)", w.Body.String(), err)
	}
	call("GET", "/resources/workstation/history?resolution=minute", "", admin, "", 200)
	call("GET", "/resources/workstation/events", "", admin, "", 200)
	call("GET", "/resources/workstation/capabilities", "", admin, "", 200)
	call("GET", "/resources/workstation/history?cursor=broken", "", admin, "", 400)
	w = call("GET", "/resources/workstation/maintenance/preview", "", admin, "", 200)
	var preview machine.MaintenancePreview
	if err = json.Unmarshal(w.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if !preview.PartialCoverage || !preview.ControllerOffline || !preview.ManualPowerOn {
		t.Fatal("missing impact disclosure")
	}
	call("POST", "/resources/workstation/maintenance", `{"reason":"ready","previewHash":"short"}`, admin, "1", 422)
	call("POST", "/resources/workstation/maintenance", `{"reason":"ready","acknowledged":true}`, admin, "1", 400)
	body := `{"reason":"workloads prepared","previewHash":"` + preview.Hash + `"}`
	h.putService(checkedService)
	call("POST", "/resources/workstation/maintenance", body, admin, "1", 409)
	w = call("GET", "/resources/workstation/maintenance/preview", "", admin, "", 200)
	if err = json.Unmarshal(w.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if len(preview.Workloads) != 1 || preview.Workloads[0].Name != "cache" {
		t.Fatalf("impact missing %+v", preview)
	}
	body = `{"reason":"workloads prepared","previewHash":"` + preview.Hash + `"}`
	call("POST", "/resources/workstation/maintenance", body, admin, "1", 200)
	call("PUT", "/resources/workstation", doc, admin, "1", 409)
	call("PUT", "/policies/blocked", `{"apiVersion":"lcm/v2","kind":"Policy","metadata":{"name":"blocked"},"spec":{"nodes":[{"id":"start","type":"trigger.manual"}]}}`, admin, "", 409)
	call("PUT", "/modules/blocked", `{"apiVersion":"lcm/v2","kind":"Module","metadata":{"name":"blocked"},"spec":{"nodes":[{"id":"in","type":"module.input","next":{"out":["done"]}},{"id":"done","type":"module.output","props":{"port":"ok"}}]}}`, admin, "", 409)
	if _, err = h.app.Resources.Put(h.ctx, []byte("apiVersion: lcm/v2\nkind: Resource\nmetadata: {name: blocked}\nspec: {type: service, adapter: command, config: {start: 'true'}}"), 0); !errors.Is(err, maintenance.ErrActive) {
		t.Fatalf("catalog bypassed gate: %v", err)
	}
	call("POST", "/resources/workstation/maintenance/exit", `{"reason":"ready"}`, admin, "1", 409)
	reboot := `{"action":"reboot","idempotencyKey":"http-once","reason":"kernel update","confirm":"workstation"}`
	w = call("POST", "/resources/workstation/operations", reboot, admin, "1", 202)
	var op machine.Operation
	if err = json.Unmarshal(w.Body.Bytes(), &op); err != nil {
		t.Fatal(err)
	}
	if op.State != "observing" || power.submits != 1 {
		t.Fatalf("operation %+v submits=%d", op, power.submits)
	}
	call("POST", "/resources/workstation/operations", reboot, admin, "1", 202)
	call("POST", "/resources/workstation/operations", `{"action":"power-on","idempotencyKey":"unsupported","reason":"r","confirm":"workstation"}`, admin, "1", 409)
	// The detail shows the operation while its outcome is open.
	w = call("GET", "/resources/workstation", "", admin, "", 200)
	var detail struct {
		Operation *machine.Operation `json:"operation"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &detail); err != nil || detail.Operation == nil || detail.Operation.ID != op.ID {
		t.Fatalf("detail operation = %s (%v)", w.Body.String(), err)
	}
	if power.submits != 1 {
		t.Fatal("duplicate OS request")
	}
	h.reopen(clk)
	if _, release, err := h.app.Maintenance.Acquire(h.ctx); err == nil {
		release()
		t.Fatal("restart cleared maintenance")
	}
	// Recovery remains available through HTTP while producers wait for maintenance.
	call("GET", "/resources/workstation/operations/"+op.ID, "", admin, "", 200)
	call("GET", "/health", "", "", "", http.StatusOK)
}

// A machine restored on other hardware reads a changed identity until an
// administrator accepts it; the resource keeps its ID and history.
func TestMachineIdentityRebindThroughHTTP(t *testing.T) {
	clk := clock.NewFake(epoch)
	probe := &machineProbe{}
	h := newHarness(t, func(o *testOpts) {
		o.clk = clk
		o.machineObserver = probe
		o.machinePower = &machinePowerFake{clk: clk}
	})
	_, admin, err := h.app.Auth.CreateToken(h.ctx, "machine-admin", auth.ScopeAdmin)
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path, body, version string, want int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+admin)
		if version != "" {
			r.Header.Set("If-Match", version)
		}
		w := httptest.NewRecorder()
		h.app.Handler().ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: got %d want %d: %s", method, path, w.Code, want, w.Body.String())
		}
		return w
	}
	doc := `{"apiVersion":"lcm/v2","kind":"Resource","metadata":{"name":"workstation"},"spec":{"type":"machine","adapter":"local"}}`
	var created struct{ ID string }
	if err = json.Unmarshal(call("POST", "/resources", doc, "", 201).Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	observe := func() {
		clk.Advance(30 * time.Second)
		call("POST", "/resources/workstation/observe", "", "", 200)
	}
	observe()
	rebind := `{"reason":"host moved","confirm":"workstation"}`
	call("POST", "/resources/workstation/identity/rebind", rebind, "1", 409)
	probe.device, probe.boot = "synthetic-new-device", "boot-new"
	observe()
	identity := func() string {
		var d struct {
			Machine struct{ Status struct{ Identity string } }
			Actions []struct {
				Name      string
				Available bool
			}
		}
		if err := json.Unmarshal(call("GET", "/resources/workstation", "", "", 200).Body.Bytes(), &d); err != nil {
			t.Fatal(err)
		}
		for _, a := range d.Actions {
			if a.Name == "rebind-identity" && a.Available != (d.Machine.Status.Identity == "changed") {
				t.Fatalf("rebind-identity available = %v with identity %s", a.Available, d.Machine.Status.Identity)
			}
		}
		return d.Machine.Status.Identity
	}
	if got := identity(); got != "changed" {
		t.Fatalf("identity = %s", got)
	}
	call("POST", "/resources/workstation/identity/rebind", `{"reason":"host moved","confirm":"other"}`, "1", 409)
	call("POST", "/resources/workstation/identity/rebind", rebind, "", 400)
	call("POST", "/resources/workstation/identity/rebind", rebind, "2", 409)
	clk.Advance(30 * time.Second)
	w := call("POST", "/resources/workstation/identity/rebind", rebind, "1", 200)
	var detail struct{ ID string }
	if err = json.Unmarshal(w.Body.Bytes(), &detail); err != nil || detail.ID != created.ID {
		t.Fatalf("detail %s (%v)", w.Body.String(), err)
	}
	if got := identity(); got != "verified" {
		t.Fatalf("identity = %s", got)
	}
	var entries int
	if err = h.app.DB.Reader().QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action='machine.identity.rebind' AND json_extract(details,'$.acceptedIdentity')='synthetic-new-device' AND json_extract(details,'$.previousIdentity')='synthetic-device'`).Scan(&entries); err != nil || entries != 1 {
		t.Fatalf("audit entries %d, %v", entries, err)
	}
	observe()
	if got := identity(); got != "verified" {
		t.Fatalf("identity after the next sample = %s", got)
	}
}

// One confirmed request enters maintenance and submits the power request;
// a refused request leaves no maintenance behind.
func TestMachinePowerSingleRequest(t *testing.T) {
	clk := clock.NewFake(epoch)
	power := &machinePowerFake{clk: clk}
	h := newHarness(t, func(o *testOpts) { o.clk = clk; o.machineObserver = &machineProbe{}; o.machinePower = power })
	_, admin, err := h.app.Auth.CreateToken(h.ctx, "machine-admin", auth.ScopeAdmin)
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path, body, version string, want int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+admin)
		if version != "" {
			r.Header.Set("If-Match", version)
		}
		w := httptest.NewRecorder()
		h.app.Handler().ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: got %d want %d: %s", method, path, w.Code, want, w.Body.String())
		}
		return w
	}
	observe := func() {
		t.Helper()
		for range 2 {
			clk.Advance(30 * time.Second)
			call("POST", "/resources/workstation/observe", "", "", 200)
		}
	}
	noMaintenance := func(step string) {
		t.Helper()
		if w := call("GET", "/resources/workstation/maintenance", "", "", 200); !strings.Contains(w.Body.String(), `"maintenance":null`) {
			t.Fatalf("%s left maintenance behind: %s", step, w.Body.String())
		}
	}
	call("POST", "/resources", `{"apiVersion":"lcm/v2","kind":"Resource","metadata":{"name":"workstation"},"spec":{"type":"machine","adapter":"local"}}`, "", 201)
	h.putService(checkedService)
	observe()
	request := func(key, confirm string) string {
		return `{"action":"reboot","idempotencyKey":"` + key + `","reason":"kernel update","confirm":"` + confirm + `"}`
	}

	call("POST", "/resources/workstation/operations", request("wrong-name", "laptop"), "1", 409)
	noMaintenance("wrong name")
	call("POST", "/resources/workstation/operations", `{"action":"reboot","idempotencyKey":"no-reason","confirm":"workstation"}`, "1", 409)
	noMaintenance("missing reason")
	power.preflightErr = errors.New("helper unavailable")
	call("POST", "/resources/workstation/operations", request("helper", "workstation"), "1", 409)
	noMaintenance("unready helper")
	power.preflightErr = nil
	clk.Advance(10 * time.Minute)
	call("POST", "/resources/workstation/operations", request("stale", "workstation"), "1", 409)
	noMaintenance("stale observations")
	if power.submits != 0 {
		t.Fatalf("refused requests reached the helper: %d", power.submits)
	}

	observe()
	w := call("POST", "/resources/workstation/operations", request("single", "workstation"), "1", 202)
	var op machine.Operation
	if err = json.Unmarshal(w.Body.Bytes(), &op); err != nil || op.State != "observing" || op.RequestReason != "kernel update" || power.submits != 1 {
		t.Fatalf("operation %s (%v) submits=%d", w.Body.String(), err, power.submits)
	}
	w = call("GET", "/resources/workstation/maintenance", "", "", 200)
	var got struct{ Maintenance *machine.Maintenance }
	if err = json.Unmarshal(w.Body.Bytes(), &got); err != nil || got.Maintenance == nil || got.Maintenance.Action != "reboot" ||
		len(got.Maintenance.Preview.Workloads) != 1 || got.Maintenance.Preview.Workloads[0].Name != "cache" {
		t.Fatalf("maintenance %s (%v)", w.Body.String(), err)
	}
	// The replay, as a client sends it after a lost answer, returns the same operation.
	w = call("POST", "/resources/workstation/operations", request("single", "workstation"), "1", 202)
	var replay machine.Operation
	if err = json.Unmarshal(w.Body.Bytes(), &replay); err != nil || replay.ID != op.ID || power.submits != 1 {
		t.Fatalf("replay %s (%v) submits=%d", w.Body.String(), err, power.submits)
	}
	// The automatic maintenance stays until an explicit exit, which the open outcome refuses.
	call("POST", "/resources/workstation/maintenance/exit", `{"reason":"done"}`, "1", 409)
}

func TestDeferredRecoveryWithPartialErrorReleasesProducers(t *testing.T) {
	probe := &machineProbe{}
	h := newHarness(t, func(o *testOpts) { o.machineObserver = probe })
	h.deploy("apiVersion: lcm/v2\nkind: Policy\nmetadata: {name: broken}\nspec: {nodes: [{id: start, type: trigger.manual}]}\n")
	if _, err := h.app.Resources.Put(h.ctx, []byte("apiVersion: lcm/v2\nkind: Resource\nmetadata: {name: workstation}\nspec: {type: machine, adapter: local}"), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.Machines.EnterMaintenance(machine.DirectRequest(h.ctx), "workstation", "prepared", 1, ""); err != nil {
		t.Fatal(err)
	}
	if err := h.app.DB.Write(h.ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE policy_versions SET document='{}' WHERE name='broken'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.app.Close(h.ctx); err != nil {
		t.Fatal(err)
	}
	h.app = h.open(testOpts{dir: h.dir, clk: h.clk, runner: h.runner, machineObserver: probe})
	for range 2 {
		h.clk.Advance(30 * time.Second)
		if _, err := h.app.Machines.Observe(h.ctx, "workstation"); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.app.Machines.ExitMaintenance(machine.DirectRequest(h.ctx), "workstation", "verified", 1); err != nil {
		t.Fatal(err)
	}
	h.run()
	h.waitTimers(loopTimers)
}
