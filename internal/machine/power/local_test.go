package power

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/execx"
)

func systemFixture() (*localSystem, *execx.Fake) {
	f := &execx.Fake{}
	f.Reply("--version", "systemd 258 (258.1)\n+PAM", 0).Reply("is-enabled", "enabled", 0).Reply("CanReboot", `{"type":"s","data":["yes"]}`, 0).Reply("CanPowerOff", `{"type":"s","data":["yes"]}`, 0).Reply("ListSessions", `{"type":"a(susso)","data":[[]]}`, 0).Reply("ListInhibitors", `{"type":"a(ssssuu)","data":[[]]}`, 0)
	return &localSystem{runner: f, version: 258}, f
}
func TestFixedCommands(t *testing.T) {
	for _, action := range []Action{Reboot, GracefulShutdown} {
		s, f := systemFixture()
		if err := s.Check(context.Background(), action); err != nil {
			t.Fatal(err)
		}
		if err := s.Request(context.Background(), action); err != nil {
			t.Fatal(err)
		}
		calls := f.Calls()
		last := calls[len(calls)-1]
		verb := "reboot"
		if action == GracefulShutdown {
			verb = "poweroff"
		}
		expected := []string{"/usr/bin/systemctl", "--system", "--no-ask-password", "--check-inhibitors=yes", verb}
		if !slices.Equal(last.Argv, expected) {
			t.Fatal(last.Argv)
		}
		for _, c := range calls {
			if c.Shell != "" || c.Timeout <= 0 || c.MaxOutput <= 0 || strings.Contains(c.Display(), "--force") {
				t.Fatal(c)
			}
		}
	}
}
func TestSystemFailClosed(t *testing.T) {
	cases := []struct {
		match, output string
		code          int
	}{
		{"--version", "systemd 247", 0}, {"--version", "systemd 259", 0}, {"--version", "unexpected", 0},
		{"is-enabled", "disabled", 0}, {"is-enabled", "enabled-runtime", 0},
		{"CanReboot", `{"type":"s","data":["challenge"]}`, 0},
		{"CanReboot", `{"type":"s","data":["yes"]}`, 1},
		{"CanReboot", `{"type":"s","data":[]}`, 0},
		{"ListSessions", `{"type":"a(susso)","data":[[["x",1000,"user","seat0","/session"]]]}`, 0},
		{"ListSessions", `{"type":"a(susso)","data":[null]}`, 0},
		{"ListInhibitors", `{"type":"a(ssssuu)","data":[[["shutdown","test","busy","block",1000,20]]]}`, 0},
		{"ListInhibitors", `{"type":"a(ssssuu)","data":[[["shutdown","test","busy","delay",1000,20]]]}`, 0},
		{"ListInhibitors", `{"type":"wrong","data":[[]]}`, 0},
	}
	for i, tc := range cases {
		t.Run(string(rune('A'+i)), func(t *testing.T) {
			f := (&execx.Fake{}).Reply(tc.match, tc.output, tc.code)
			defaults, _ := systemFixture()
			defaultFake := defaults.runner.(*execx.Fake)
			f.On(func(execx.Cmd) bool { return true }, func(c execx.Cmd) (execx.Result, error) { return defaultFake.Run(context.Background(), c) })
			s := &localSystem{runner: f, version: 258}
			if err := s.Check(context.Background(), Reboot); err == nil {
				t.Fatal("unsafe system accepted")
			}
		})
	}
	s, f := systemFixture()
	if err := s.Request(context.Background(), Action("power-on")); err == nil || len(f.Calls()) != 0 {
		t.Fatal("unsupported action executed")
	}
	s.runner = (&execx.Fake{}).On(func(execx.Cmd) bool { return true }, func(execx.Cmd) (execx.Result, error) { return execx.Result{Truncated: true}, nil })
	if err := s.Check(context.Background(), Reboot); err == nil {
		t.Fatal("truncated version accepted")
	}
}
func TestClientUsesBoundedStructuredStdin(t *testing.T) {
	f := (&execx.Fake{}).On(func(execx.Cmd) bool { return true }, func(c execx.Cmd) (execx.Result, error) {
		req, err := DecodeRequest(strings.NewReader(string(c.Stdin)))
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(c.Argv, []string{"/usr/bin/sudo", "-n", "--", WrapperPath}) || c.Shell != "" || c.Timeout <= 0 || c.MaxOutput != MaxMessage {
			t.Fatal(c)
		}
		if strings.Contains(c.Display(), req.Binding.OperationID) {
			t.Fatal("binding exposed in argv")
		}
		raw, _ := json.Marshal(Response{Version: Version, Receipt: &Receipt{Binding: req.Binding, State: Accepted, UpdatedAt: 1000000}})
		return execx.Result{Output: string(raw)}, nil
	})
	result, err := (Client{Runner: f}).Call(context.Background(), request(Prepare))
	if err != nil {
		t.Fatal(err)
	}
	mustReceipt(t, result, Accepted, false)
}
func TestClientRejectsUncertainOrMismatchedResponses(t *testing.T) {
	for _, kind := range []string{"malformed", "version", "binding", "truncated", "exit", "state", "missing", "both"} {
		t.Run(kind, func(t *testing.T) {
			response := Response{Version: Version, Receipt: &Receipt{Binding: binding(), State: Accepted, UpdatedAt: 1000000}}
			switch kind {
			case "version":
				response.Version = 2
			case "binding":
				response.Receipt.Binding.BootID = controller
			case "state":
				response.Receipt.State = "succeeded"
			case "missing":
				response.Receipt = nil
			case "both":
				response.Code = "bad"
			}
			raw, _ := json.Marshal(response)
			f := (&execx.Fake{}).On(func(execx.Cmd) bool { return true }, func(execx.Cmd) (execx.Result, error) {
				result := execx.Result{Output: string(raw)}
				switch kind {
				case "malformed":
					result.Output = "garbage"
				case "truncated":
					result.Truncated = true
				case "exit":
					return result, errors.New("lost process")
				}
				return result, nil
			})
			if _, err := (Client{Runner: f}).Call(context.Background(), request(Submit)); err == nil {
				t.Fatal("unsafe response accepted")
			}
		})
	}
}

func TestDeadlineInput(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close(); _ = writer.Close() }()
	input, err := deadlineInput(reader, time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = input.Close() }()
	if _, err := input.Read(make([]byte, 1)); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("unbounded pipe: %v", err)
	}
	if _, err := deadlineInput(strings.NewReader("{}"), time.Unix(1, 0)); err == nil {
		t.Fatal("unbounded reader accepted")
	}
	file, err := os.CreateTemp(t.TempDir(), "input")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	if _, err := deadlineInput(file, time.Unix(1, 0)); err == nil {
		t.Fatal("regular file accepted")
	}
}
