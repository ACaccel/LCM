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

type machineProbe struct{ n uint64 }

func (p *machineProbe) Observe(context.Context, model.MachineConfig) (machine.Snapshot, error) {
	p.n += 100
	s := machine.Snapshot{DeviceHash: "synthetic-device", BootID: "boot-a", Init: "systemd", CPUTotal: p.n, CPUIdle: p.n * 9 / 10, CPUCount: 4, MemoryTotal: 100, MemoryAvailable: 90, Filesystems: []machine.Filesystem{{Path: "/", Total: 100, Available: 90}}, Coverage: map[string]machine.Coverage{}}
	for _, key := range []string{"identity", "boot", "init", "uptime", "cpu", "memory"} {
		s.Coverage[key] = machine.Coverage{State: "available"}
	}
	return s, nil
}
func (*machineProbe) ReadJournal(context.Context, model.MachineConfig, string, time.Time) (machine.JournalBatch, error) {
	return machine.JournalBatch{}, nil
}

type machinePowerFake struct {
	submits int
	receipt machine.Receipt
	clk     *clock.Fake
}

func (*machinePowerFake) Preflight(context.Context) error { return nil }
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
			Machine          *machine.Snapshot
		}
	}
	if err = json.Unmarshal(w.Body.Bytes(), &hist); err != nil || len(hist.Items) != 2 || hist.Items[0].Type != "machine" ||
		hist.Items[0].Machine == nil || hist.Items[0].ResourceID == "" {
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
	call("POST", "/resources/workstation/maintenance", `{"reason":"ready","acknowledged":true}`, admin, "1", 422)
	body := `{"reason":"workloads prepared","acknowledged":true,"previewHash":"` + preview.Hash + `"}`
	h.putService(checkedService)
	call("POST", "/resources/workstation/maintenance", body, admin, "1", 409)
	w = call("GET", "/resources/workstation/maintenance/preview", "", admin, "", 200)
	if err = json.Unmarshal(w.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if len(preview.Workloads) != 1 || preview.Workloads[0].Name != "cache" {
		t.Fatalf("impact missing %+v", preview)
	}
	body = `{"reason":"workloads prepared","acknowledged":true,"previewHash":"` + preview.Hash + `"}`
	call("POST", "/resources/workstation/maintenance", body, admin, "1", 200)
	call("PUT", "/resources/workstation", doc, admin, "1", 409)
	call("PUT", "/policies/blocked", `{"apiVersion":"lcm/v2","kind":"Policy","metadata":{"name":"blocked"},"spec":{"nodes":[{"id":"start","type":"trigger.manual"}]}}`, admin, "", 409)
	call("PUT", "/modules/blocked", `{"apiVersion":"lcm/v2","kind":"Module","metadata":{"name":"blocked"},"spec":{"nodes":[{"id":"in","type":"module.input","next":{"out":["done"]}},{"id":"done","type":"module.output","props":{"port":"ok"}}]}}`, admin, "", 409)
	if _, err = h.app.Resources.Put(h.ctx, []byte("apiVersion: lcm/v2\nkind: Resource\nmetadata: {name: blocked}\nspec: {type: service, adapter: command, config: {start: 'true'}}"), 0); !errors.Is(err, maintenance.ErrActive) {
		t.Fatalf("catalog bypassed gate: %v", err)
	}
	call("POST", "/resources/workstation/maintenance/exit", `{"reason":"ready"}`, admin, "1", 409)
	w = call("POST", "/resources/workstation/operations", `{"action":"reboot","idempotencyKey":"http-once"}`, admin, "1", 202)
	var op machine.Operation
	if err = json.Unmarshal(w.Body.Bytes(), &op); err != nil {
		t.Fatal(err)
	}
	if op.State != "observing" || power.submits != 1 {
		t.Fatalf("operation %+v submits=%d", op, power.submits)
	}
	call("POST", "/resources/workstation/operations", `{"action":"reboot","idempotencyKey":"http-once"}`, admin, "1", 202)
	call("POST", "/resources/workstation/operations", `{"action":"power-on","idempotencyKey":"unsupported"}`, admin, "1", 409)
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

func TestDeferredRecoveryWithPartialErrorReleasesProducers(t *testing.T) {
	probe := &machineProbe{}
	h := newHarness(t, func(o *testOpts) { o.machineObserver = probe })
	h.deploy("apiVersion: lcm/v2\nkind: Policy\nmetadata: {name: broken}\nspec: {nodes: [{id: start, type: trigger.manual}]}\n")
	if _, err := h.app.Resources.Put(h.ctx, []byte("apiVersion: lcm/v2\nkind: Resource\nmetadata: {name: workstation}\nspec: {type: machine, adapter: local}"), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.Machines.EnterMaintenance(machine.DirectRequest(h.ctx), "workstation", "prepared", true, 1); err != nil {
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
