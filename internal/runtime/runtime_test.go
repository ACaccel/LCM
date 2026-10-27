package runtime_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ACaccel/lcm/internal/execx"
	"github.com/ACaccel/lcm/internal/model"
	rt "github.com/ACaccel/lcm/internal/runtime"
)

func svc(t *testing.T, runtimeYAML string) *model.Service {
	t.Helper()
	s, err := model.DecodeService([]byte("apiVersion: lcm/v1\nkind: Service\nmetadata: { name: app }\nspec:\n  runtime:\n    " + runtimeYAML + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func adapter(t *testing.T, f *execx.Fake, s *model.Service) rt.Adapter {
	t.Helper()
	a, err := rt.NewRegistry(f, rt.Options{}).For(s)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestSystemdStatus(t *testing.T) {
	tests := []struct {
		name, out string
		want      rt.State
	}{
		{"active", "LoadState=loaded\nActiveState=active\nSubState=running\nMainPID=42\nActiveEnterTimestamp=Mon 2026-01-01 00:00:00 UTC\n", rt.StateActive},
		{"reloading", "LoadState=loaded\nActiveState=reloading\nSubState=reload\nMainPID=42\n", rt.StateActive},
		{"inactive", "LoadState=loaded\nActiveState=inactive\nSubState=dead\nMainPID=0\n", rt.StateInactive},
		{"failed", "LoadState=loaded\nActiveState=failed\nSubState=failed\nMainPID=0\n", rt.StateFailed},
		{"activating", "LoadState=loaded\nActiveState=activating\nSubState=auto-restart\nMainPID=0\n", rt.StateUnknown},
	}
	s := svc(t, "type: systemd\n    unit: app.service")
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := (&execx.Fake{}).Reply("systemctl show", tt.out, 0)
			st, err := adapter(t, f, s).Status(context.Background(), s)
			if err != nil || st.State != tt.want {
				t.Fatalf("got %+v %v, want %s", st, err, tt.want)
			}
			want := "systemctl show -p LoadState,ActiveState,SubState,MainPID,ActiveEnterTimestamp app.service"
			if got := f.Displays(); len(got) != 1 || got[0] != want {
				t.Fatalf("argv = %v", got)
			}
		})
	}

	f := (&execx.Fake{}).Reply("systemctl show", "LoadState=loaded\nActiveState=active\nSubState=running\nMainPID=42\nActiveEnterTimestamp=then\n", 0)
	st, _ := adapter(t, f, s).Status(context.Background(), s)
	if st.Detail["pid"] != "42" || st.Detail["subState"] != "running" || st.Detail["since"] != "then" {
		t.Fatalf("detail = %v", st.Detail)
	}
}

func TestSystemdStatusErrors(t *testing.T) {
	s := svc(t, "type: systemd\n    unit: ghost.service")
	f := (&execx.Fake{}).Reply("systemctl show", "LoadState=not-found\nActiveState=inactive\nSubState=dead\n", 0)
	st, err := adapter(t, f, s).Status(context.Background(), s)
	if !errors.Is(err, rt.ErrNotFound) || st.State != rt.StateUnknown {
		t.Fatalf("got %+v %v", st, err)
	}

	f = (&execx.Fake{}).Reply("systemctl show", "garbage", 0)
	if _, err := adapter(t, f, s).Status(context.Background(), s); err == nil {
		t.Fatal("garbage accepted")
	}
	f = (&execx.Fake{}).Reply("systemctl show", "boom", 1)
	if st, err := adapter(t, f, s).Status(context.Background(), s); err == nil || st.State != rt.StateUnknown || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("got %+v %v", st, err)
	}
}

func TestSystemdActionsArgv(t *testing.T) {
	tests := []struct {
		name, runtime string
		want          []string
	}{
		{"system default sudo", "type: systemd\n    unit: app.service", []string{
			"sudo -n systemctl start app.service", "sudo -n systemctl stop app.service", "sudo -n systemctl restart app.service"}},
		{"user no sudo", "type: systemd\n    unit: app.service\n    scope: user", []string{
			"systemctl --user start app.service", "systemctl --user stop app.service", "systemctl --user restart app.service"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := svc(t, tt.runtime)
			f := &execx.Fake{}
			a := adapter(t, f, s)
			ctx := context.Background()
			for _, err := range []error{a.Start(ctx, s), a.Stop(ctx, s), a.Restart(ctx, s)} {
				if err != nil {
					t.Fatal(err)
				}
			}
			if got := f.Displays(); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("argv = %v, want %v", got, tt.want)
			}
			for _, c := range f.Calls() {
				if c.Shell != "" {
					t.Fatal("systemd adapter must not use a shell")
				}
			}
		})
	}
}

func TestSystemdCustomPrivilegeAndFailure(t *testing.T) {
	s := svc(t, "type: systemd\n    unit: app.service")
	f := (&execx.Fake{}).Reply("restart", "Interactive authentication required.", 1)
	a, _ := rt.NewRegistry(f, rt.Options{Privilege: []string{"doas"}, Systemctl: "/usr/bin/systemctl"}).For(s)
	err := a.Restart(context.Background(), s)
	if err == nil || !strings.Contains(err.Error(), "authentication required") {
		t.Fatalf("want error with output, got %v", err)
	}
	if got := f.Displays()[0]; got != "doas /usr/bin/systemctl restart app.service" {
		t.Fatalf("argv = %q", got)
	}

	// An empty, non-nil prefix disables privilege escalation.
	f = &execx.Fake{}
	a, _ = rt.NewRegistry(f, rt.Options{Privilege: []string{}}).For(s)
	_ = a.Start(context.Background(), s)
	if got := f.Displays()[0]; got != "systemctl start app.service" {
		t.Fatalf("argv = %q", got)
	}
}

func TestDocker(t *testing.T) {
	s := svc(t, "type: docker\n    container: web")
	tests := []struct {
		out  string
		want rt.State
	}{
		{"running|0|123|2026-01-01T00:00:00Z|nginx:1", rt.StateActive},
		{"exited|0|0|2026-01-01T00:00:00Z|nginx:1", rt.StateInactive},
		{"exited|137|0|2026-01-01T00:00:00Z|nginx:1", rt.StateFailed},
		{"created|0|0||nginx:1", rt.StateInactive},
		{"dead|0|0||nginx:1", rt.StateFailed},
		{"restarting|1|0||nginx:1", rt.StateUnknown},
		{"paused|0|5||nginx:1", rt.StateUnknown},
	}
	for _, tt := range tests {
		f := (&execx.Fake{}).Reply("docker inspect", tt.out+"\n", 0)
		st, err := adapter(t, f, s).Status(context.Background(), s)
		if err != nil || st.State != tt.want || st.Detail["image"] != "nginx:1" {
			t.Fatalf("%q: got %+v %v, want %s", tt.out, st, err, tt.want)
		}
	}

	f := (&execx.Fake{}).Reply("docker inspect", "Error: No such container: web", 1)
	if _, err := adapter(t, f, s).Status(context.Background(), s); !errors.Is(err, rt.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	f = (&execx.Fake{}).Reply("docker inspect", "weird", 0)
	if _, err := adapter(t, f, s).Status(context.Background(), s); err == nil {
		t.Fatal("malformed output accepted")
	}

	f = &execx.Fake{}
	a := adapter(t, f, s)
	ctx := context.Background()
	_ = a.Start(ctx, s)
	_ = a.Stop(ctx, s)
	_ = a.Restart(ctx, s)
	if got, want := f.Displays(), []string{"docker start web", "docker stop web", "docker restart web"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %v", got)
	}
}

func TestCompose(t *testing.T) {
	s := svc(t, "type: compose\n    project: shop\n    file: /srv/shop/compose.yaml")
	tests := []struct {
		name, out string
		want      rt.State
	}{
		{"ndjson running", `{"Service":"web","State":"running","ExitCode":0}` + "\n" + `{"Service":"db","State":"running","ExitCode":0}` + "\n", rt.StateActive},
		{"array running", `[{"Service":"web","State":"running","ExitCode":0}]`, rt.StateActive},
		{"empty", "", rt.StateInactive},
		{"all exited clean", `{"Service":"web","State":"exited","ExitCode":0}`, rt.StateInactive},
		{"sidecar crashed", `{"Service":"web","State":"running"}` + "\n" + `{"Service":"db","State":"exited","ExitCode":1}`, rt.StateFailed},
		{"init job done", `{"Service":"web","State":"running"}` + "\n" + `{"Service":"init","State":"exited","ExitCode":0}`, rt.StateActive},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := (&execx.Fake{}).Reply("compose", tt.out, 0)
			st, err := adapter(t, f, s).Status(context.Background(), s)
			if err != nil || st.State != tt.want {
				t.Fatalf("got %+v %v, want %s", st, err, tt.want)
			}
			if got := f.Displays()[0]; got != "docker compose -p shop -f /srv/shop/compose.yaml ps -a --format json" {
				t.Fatalf("argv = %q", got)
			}
		})
	}
	f := (&execx.Fake{}).Reply("compose", "{not json", 0)
	if _, err := adapter(t, f, s).Status(context.Background(), s); err == nil {
		t.Fatal("malformed output accepted")
	}

	f = &execx.Fake{}
	a := adapter(t, f, s)
	ctx := context.Background()
	_ = a.Start(ctx, s)
	_ = a.Stop(ctx, s)
	_ = a.Restart(ctx, s)
	pre := "docker compose -p shop -f /srv/shop/compose.yaml "
	if got, want := f.Displays(), []string{pre + "up -d", pre + "stop", pre + "restart"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %v", got)
	}
}

func TestCommand(t *testing.T) {
	s := svc(t, "type: command\n    start: ./start.sh\n    stop: ./stop.sh\n    status: ./status.sh")
	ctx := context.Background()

	f := (&execx.Fake{}).Reply("status.sh", "", 0)
	if st, err := adapter(t, f, s).Status(ctx, s); err != nil || st.State != rt.StateActive {
		t.Fatalf("exit 0: %+v %v", st, err)
	}
	f = (&execx.Fake{}).Reply("status.sh", "", 3)
	if st, err := adapter(t, f, s).Status(ctx, s); err != nil || st.State != rt.StateInactive {
		t.Fatalf("exit 3: %+v %v", st, err)
	}
	f = (&execx.Fake{}).On(func(execx.Cmd) bool { return true }, func(execx.Cmd) (execx.Result, error) {
		return execx.Result{ExitCode: -1}, errors.New("sh: not found")
	})
	if st, err := adapter(t, f, s).Status(ctx, s); err == nil || st.State != rt.StateUnknown {
		t.Fatalf("start failure must be unknown+error: %+v %v", st, err)
	}

	// Restart without a restart command is stop then start.
	f = &execx.Fake{}
	a := adapter(t, f, s)
	if err := a.Restart(ctx, s); err != nil {
		t.Fatal(err)
	}
	if got := f.Displays(); !reflect.DeepEqual(got, []string{"sh -c ./stop.sh", "sh -c ./start.sh"}) {
		t.Fatalf("calls = %v", got)
	}
	// A failing stop prevents the start.
	f = (&execx.Fake{}).Reply("stop.sh", "cannot stop", 1)
	if err := adapter(t, f, s).Restart(ctx, s); err == nil || len(f.Calls()) != 1 {
		t.Fatalf("want stop failure only, got %v %v", err, f.Displays())
	}

	with := svc(t, "type: command\n    start: a\n    stop: b\n    restart: c")
	f = &execx.Fake{}
	if err := adapter(t, f, with).Restart(ctx, with); err != nil || !reflect.DeepEqual(f.Displays(), []string{"sh -c c"}) {
		t.Fatalf("restart command: %v %v", err, f.Displays())
	}
	if st, err := adapter(t, f, with).Status(ctx, with); err != nil || st.State != rt.StateUnknown {
		t.Fatalf("no status command: %+v %v", st, err)
	}
}

func TestExternal(t *testing.T) {
	s := svc(t, "type: external")
	f := &execx.Fake{}
	a := adapter(t, f, s)
	ctx := context.Background()
	if st, err := a.Status(ctx, s); err != nil || st.State != rt.StateUnknown {
		t.Fatalf("status: %+v %v", st, err)
	}
	for _, err := range []error{a.Start(ctx, s), a.Stop(ctx, s), a.Restart(ctx, s)} {
		if !errors.Is(err, rt.ErrUnsupported) {
			t.Fatalf("want ErrUnsupported, got %v", err)
		}
	}
	if len(f.Calls()) != 0 {
		t.Fatal("external adapter must not run commands")
	}
}
