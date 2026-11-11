package deployhelper

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/execx"
)

const opID = `"operationId":"0b7c4a1e-3f2d-4c5b-9a8e-123456789abc"`

func TestDecodeRequest(t *testing.T) {
	sha := strings.Repeat("a", 40)
	for _, in := range []string{
		`{"version":1,"op":"build",` + opID + `,"service":"web","commit":"` + sha + `","command":"make","timeoutSec":60}`,
		`{"version":1,"op":"build",` + opID + `,"service":"web","commit":"` + strings.Repeat("b", 64) + `","command":"make","timeoutSec":7200,"runtimes":["node24","go"]}`,
		`{"version":1,"op":"install",` + opID + `,"service":"web","buildId":"20261109T120000Z-0a1b2c3d","artifact":"dist/web"}`,
		`{"version":1,"op":"install",` + opID + `,"service":"web","buildId":"20261109T120000Z-0a1b2c3d","artifact":"."}`,
		`{"version":1,"op":"activate",` + opID + `,"service":"web","releaseId":"20261109T120000Z-0a1b2c3d","start":["server/main.mjs","--port=env:PORT"],"runtime":"node24","migrate":["node","scripts/migrate.mjs"],"keepReleases":5}`,
		`{"version":1,"op":"activate",` + opID + `,"service":"web","releaseId":"20261109T120000Z-0a1b2c3d","start":["bin/web"],"migrate":["bin/migrate"]}`,
		`{"version":1,"op":"activate",` + opID + `,"service":"web","releaseId":"20261109T120000Z-0a1b2c3d","start":["bin/web"],"publish":{"dir":"client/dist","retain":["assets/","media/emoji/"],"retainDays":90},"limits":{"memoryHigh":"768M","memoryMax":"16G","stopTimeoutSec":600}}`,
		`{"version":1,"op":"activate",` + opID + `,"service":"web","releaseId":"20261109T120000Z-0a1b2c3d","publish":{"dir":"."}}`,
		`{"version":1,"op":"restart",` + opID + `,"service":"web"}`,
		`{"version":1,"op":"status",` + opID + `,"service":"web"}`,
		`{"version":1,"op":"query",` + opID + `}`,
	} {
		if _, err := DecodeRequest([]byte(in)); err != nil {
			t.Errorf("%s: %v", in, err)
		}
	}
	build := `"op":"build",` + opID + `,"service":"web","commit":"` + sha + `","command":"make"`
	for in, want := range map[string]string{
		`{"version":2,"op":"query",` + opID + `}`:                                                                                                                                 "unsupported protocol version",
		`{"version":1,"op":"deploy",` + opID + `}`:                                                                                                                                `unknown op "deploy"`,
		`{"version":1,"op":"backup",` + opID + `,"service":"web"}`:                                                                                                                "timeoutSec must be between",
		`{"version":1,"op":"restore",` + opID + `}`:                                                                                                                               "service names must match",
		`{"version":1,"op":"query","operationId":"0B7C4A1E-3F2D-4C5B-9A8E-123456789ABC"}`:                                                                                         "lower-case UUID",
		`{"version":1,"op":"query"}`:                                                                                                                                              "lower-case UUID",
		`{"version":1,"op":"query",` + opID + `,"service":"web"}`:                                                                                                                 "query does not take service",
		`{"version":1,"op":"restart",` + opID + `,"service":"web","commit":"` + sha + `"}`:                                                                                        "restart does not take commit",
		`{"version":1,"op":"restart",` + opID + `,"service":"lcm"}`:                                                                                                               `must not start with "lcm"`,
		`{"version":1,"op":"restart",` + opID + `,"service":"../etc"}`:                                                                                                            "service names must match",
		`{"version":1,"op":"restart",` + opID + `,"service":"web","extra":1}`:                                                                                                     "unknown field",
		`{"version":1,"op":"restart",` + opID + `,"Service":"web"}`:                                                                                                               `unknown key "Service"`,
		`{"version":1,"op":"restart",` + opID + `,"service":"web","Service":"x"}`:                                                                                                 "duplicate key",
		`{"version":1,"op":"restart",` + opID + `,"service":"web","service":"x"}`:                                                                                                 "duplicate key",
		`{"version":1,"op":"restart",` + opID + `,"service":"web"} {}`:                                                                                                            "trailing data",
		`{"version":1,` + build + `,"timeoutSec":0}`:                                                                                                                              "timeoutSec must be between",
		`{"version":1,` + build + `,"timeoutSec":7201}`:                                                                                                                           "timeoutSec must be between",
		`{"version":1,` + build + `,"timeoutSec":60,"runtimes":["node24","node24"]}`:                                                                                              "listed twice",
		`{"version":1,` + build + `,"timeoutSec":60,"runtimes":["Node"]}`:                                                                                                         "not a runtime name",
		`{"version":1,"op":"build",` + opID + `,"service":"web","commit":"HEAD","command":"make","timeoutSec":60}`:                                                                "full lower-case commit ID",
		`{"version":1,"op":"build",` + opID + `,"service":"web","commit":"` + sha + `","command":"  ","timeoutSec":60}`:                                                           "non-empty script",
		`{"version":1,"op":"install",` + opID + `,"service":"web","buildId":"latest","artifact":"dist"}`:                                                                          "buildId is not a build ID",
		`{"version":1,"op":"install",` + opID + `,"service":"web","buildId":"20261109T120000Z-0a1b2c3d","artifact":"../x"}`:                                                       "artifact must be",
		`{"version":1,"op":"install",` + opID + `,"service":"web","buildId":"20261109T120000Z-0a1b2c3d","artifact":"/etc"}`:                                                       "artifact must be",
		`{"version":1,"op":"install",` + opID + `,"service":"web","buildId":"20261109T120000Z-0a1b2c3d","artifact":"a//b"}`:                                                       "artifact must be",
		`{"version":1,"op":"activate",` + opID + `,"service":"web","releaseId":"20261109T120000Z-0a1b2c3d"}`:                                                                      "without start needs publish",
		`{"version":1,"op":"activate",` + opID + `,"service":"web","releaseId":"20261109T120000Z-0a1b2c3d",` + `"publish":{"dir":"."},"migrate":["bin/m"]}`:                       "without start needs publish",
		`{"version":1,"op":"activate",` + opID + `,"service":"web","releaseId":"20261109T120000Z-0a1b2c3d",` + `"publish":{"dir":"."},"limits":{"memoryMax":"1G"}}`:               "without start needs publish",
		`{"version":1,"op":"activate",` + opID + `,"service":"web","releaseId":"20261109T120000Z-0a1b2c3d",` + `"start":[],"publish":{"dir":"."}}`:                                "start must begin",
		`{"version":1,"op":"activate",` + opID + `,"service":"web","releaseId":"20261109T120000Z-0a1b2c3d",` + `"publish":{"dir":"../www"}}`:                                      "dir must be",
		`{"version":1,"op":"activate",` + opID + `,"service":"web","releaseId":"20261109T120000Z-0a1b2c3d",` + `"publish":{"dir":"/srv"}}`:                                        "dir must be",
		`{"version":1,"op":"activate",` + opID + `,"service":"web","releaseId":"20261109T120000Z-0a1b2c3d",` + `"publish":{"dir":".","retain":["assets"]}}`:                       "retain",
		`{"version":1,"op":"activate",` + opID + `,"service":"web","releaseId":"20261109T120000Z-0a1b2c3d",` + `"publish":{"dir":".","retain":[".cache/"]}}`:                      "retain",
		`{"version":1,"op":"activate",` + opID + `,"service":"web","releaseId":"20261109T120000Z-0a1b2c3d",` + `"publish":{"dir":".","retain":["./"]}}`:                           "retain",
		`{"version":1,"op":"activate",` + opID + `,"service":"web","releaseId":"20261109T120000Z-0a1b2c3d",` + `"publish":{"dir":".","retain":["a/","a/"]}}`:                      "listed once",
		`{"version":1,"op":"activate",` + opID + `,"service":"web","releaseId":"20261109T120000Z-0a1b2c3d",` + `"publish":{"dir":".","retainDays":3}}`:                            "retainDays needs retain",
		`{"version":1,"op":"activate",` + opID + `,"service":"web","releaseId":"20261109T120000Z-0a1b2c3d",` + `"publish":{"dir":".","retain":["a/"],"retainDays":91}}`:           "retainDays must be",
		`{"version":1,"op":"activate",` + opID + `,"service":"web","releaseId":"20261109T120000Z-0a1b2c3d",` + `"publish":{"dir":".","retain":["a/"],"retainDays":-1}}`:           "retainDays must be",
		`{"version":1,"op":"activate",` + opID + `,"service":"web","releaseId":"20261109T120000Z-0a1b2c3d",` + `"publish":{"dir":".","extra":1}}`:                                 "unknown field",
		`{"version":1,"op":"activate",` + opID + `,"service":"web","releaseId":"20261109T120000Z-0a1b2c3d",` + `"publish":{"dir":".","dir":"x"}}`:                                 "duplicate key",
		`{"version":1,"op":"activate",` + opID + `,"service":"web","releaseId":"20261109T120000Z-0a1b2c3d",` + `"start":["a"],"limits":{"memoryMax":"1T"}}`:                       "sizes such as",
		`{"version":1,"op":"activate",` + opID + `,"service":"web","releaseId":"20261109T120000Z-0a1b2c3d",` + `"start":["a"],"limits":{"memoryMax":"512K"}}`:                     "sizes such as",
		`{"version":1,"op":"activate",` + opID + `,"service":"web","releaseId":"20261109T120000Z-0a1b2c3d",` + `"start":["a"],"limits":{"memoryMax":"17G"}}`:                      "sizes such as",
		`{"version":1,"op":"activate",` + opID + `,"service":"web","releaseId":"20261109T120000Z-0a1b2c3d",` + `"start":["a"],"limits":{"memoryMax":"1G\nExecStartPre=/bin/sh"}}`: "sizes such as",
		`{"version":1,"op":"activate",` + opID + `,"service":"web","releaseId":"20261109T120000Z-0a1b2c3d",` + `"start":["a"],"limits":{"memoryHigh":"2G","memoryMax":"1G"}}`:     "not be larger",
		`{"version":1,"op":"activate",` + opID + `,"service":"web","releaseId":"20261109T120000Z-0a1b2c3d",` + `"start":["a"],"limits":{"stopTimeoutSec":601}}`:                   "stopTimeoutSec must be",
		`{"version":1,"op":"activate",` + opID + `,"service":"web","releaseId":"20261109T120000Z-0a1b2c3d",` + `"start":["a"],"limits":{"stopTimeoutSec":-1}}`:                    "stopTimeoutSec must be",
		`{"version":1,"op":"install",` + opID + `,"service":"web","buildId":"20261109T120000Z-0a1b2c3d","artifact":"dist","publish":{"dir":"."}}`:                                 "install does not take publish",
		`{"version":1,"op":"activate",` + opID + `,"service":"web","releaseId":"20261109T120000Z-0a1b2c3d","start":["."]}`:                                                        "start must begin",
		`{"version":1,"op":"activate",` + opID + `,"service":"web","releaseId":"20261109T120000Z-0a1b2c3d","start":["a","$HOME"]}`:                                                "may only contain",
		`{"version":1,"op":"activate",` + opID + `,"service":"web","releaseId":"20261109T120000Z-0a1b2c3d","start":["a","%n"]}`:                                                   "may only contain",
		`{"version":1,"op":"activate",` + opID + `,"service":"web","releaseId":"20261109T120000Z-0a1b2c3d","start":["a","x y"]}`:                                                  "may only contain",
		`{"version":1,"op":"activate",` + opID + `,"service":"web","releaseId":"20261109T120000Z-0a1b2c3d","start":["a"],"migrate":[]}`:                                           "migrate must begin",
		`{"version":1,"op":"activate",` + opID + `,"service":"web","releaseId":"20261109T120000Z-0a1b2c3d","start":["a"],"migrate":["../m"]}`:                                     "migrate must begin",
		`{"version":1,"op":"activate",` + opID + `,"service":"web","releaseId":"20261109T120000Z-0a1b2c3d","start":["a"],"keepReleases":1}`:                                       "keepReleases must be",
		`{"version":1,"op":"activate",` + opID + `,"service":"web","releaseId":"20261109T120000Z-0a1b2c3d","start":["a"],"runtime":"Node"}`:                                       "runtime is not a runtime name",
	} {
		if _, err := DecodeRequest([]byte(in)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", in, err, want)
		}
	}
	if _, err := DecodeRequest([]byte("{\"version\":1,\"op\":\"query\",\"operationId\":\"w\xffb\"}")); err == nil || !strings.Contains(err.Error(), "invalid UTF-8") {
		t.Errorf("invalid UTF-8: %v", err)
	}
	if _, err := DecodeRequest([]byte(`{"version":1,"op":"build",` + opID + `,"service":"web","commit":"` + sha + `","timeoutSec":60,"command":"` + strings.Repeat("x", MaxRequest) + `"}`)); err == nil {
		t.Error("an oversized request was accepted")
	}
}

func TestHandleEchoesOnlyWellFormedIdentifiers(t *testing.T) {
	h := &Helper{}
	r := h.Handle(context.Background(), []byte(`{"version":1,"op":"restart",`+opID+`,"service":"LCM"}`))
	if r.Code != CodeRefused || r.Op != OpRestart || r.OperationID == "" || r.State != "" {
		t.Errorf("%+v", r)
	}
	r = h.Handle(context.Background(), []byte(`{"version":1,"op":"\u001b[2J","operationId":"x\u001b"}`))
	if r.Code != CodeRefused || r.Op != "" || r.OperationID != "" || strings.ContainsRune(r.Message, 0x1b) {
		t.Errorf("%+v", r)
	}
}

func TestEncodeResponseStaysWithinItsLimit(t *testing.T) {
	r := Response{Version: Version, Op: OpReleases, OperationID: "x", Log: strings.Repeat("<", MaxLog)}
	for range 2000 {
		r.Releases = append(r.Releases, Release{ReleaseID: strings.Repeat("r", 200)})
	}
	raw := encodeResponse(r)
	if len(raw) > MaxResponse {
		t.Fatalf("%d bytes", len(raw))
	}
	if got, err := DecodeResponse(raw); err != nil || got.Log != "" || !strings.Contains(got.Message, "too large") {
		t.Fatalf("%+v %v", got, err)
	}
	if raw := encodeResponse(Response{Version: Version, Log: "<&>"}); !strings.Contains(string(raw), `"<&>"`) {
		t.Errorf("escaped: %s", raw)
	}
}

func TestSanitizeLog(t *testing.T) {
	if got := sanitizeLog("ok\tline\n\x1b[2J\u202ebad\xff\r\n"); got != "ok\tline\n?[2J?bad??\n" {
		t.Errorf("%q", got)
	}
	long := strings.Repeat("é", MaxLog)
	if got := sanitizeLog(long); len(got) > MaxLog || !strings.HasSuffix(got, "é") || !strings.HasPrefix(got, "é") {
		t.Errorf("cut %d bytes", len(got))
	}
}

func clientReq() Request {
	return Request{Version: Version, Op: OpRestart, OperationID: "0b7c4a1e-3f2d-4c5b-9a8e-123456789abc", Service: "web"}
}

func TestClient(t *testing.T) {
	req := clientReq()
	ok := `{"version":1,"op":"restart","operationId":"0b7c4a1e-3f2d-4c5b-9a8e-123456789abc","state":"succeeded"}`
	f := (&execx.Fake{}).Reply("lcm-deploy", ok+"\n", 0)
	resp, err := Client{Runner: f}.Call(context.Background(), req)
	if err != nil || resp.State != StateSucceeded {
		t.Fatalf("%+v %v", resp, err)
	}
	c := f.Calls()[0]
	want := []string{"/usr/bin/sudo", "-n", "--", "/usr/local/libexec/lcm-deploy"}
	if strings.Join(c.Argv, " ") != strings.Join(want, " ") || c.Timeout != 8*time.Minute || c.MaxOutput != MaxResponse+1 {
		t.Errorf("command %+v", c)
	}
	var sent Request
	if err := json.Unmarshal(c.Stdin, &sent); err != nil || sent.Hash() != req.Hash() {
		t.Errorf("stdin %s", c.Stdin)
	}
	b := Request{Version: Version, Op: OpBuild, OperationID: req.OperationID, Service: "web", Commit: strings.Repeat("a", 40), Command: "make", TimeoutSec: 600}
	if Timeout(b) != 33*time.Minute {
		t.Errorf("build timeout %s", Timeout(b))
	}
	a := Request{Version: Version, Op: OpActivate, OperationID: req.OperationID, Service: "web", ReleaseID: "20261109T120000Z-0a1b2c3d", Start: []string{"x"}}
	if Timeout(a) != 44*time.Minute {
		t.Errorf("activate timeout %s", Timeout(a))
	}
	for op := range opFields {
		r := Request{Op: op, TimeoutSec: 60}
		if Timeout(r) <= budget(r) {
			t.Errorf("%s: timeout %s does not exceed the helper's budget %s", op, Timeout(r), budget(r))
		}
	}

	for name, tc := range map[string]struct {
		out     string
		code    int
		unknown bool
		code2   Code
	}{
		"refusal":            {`{"version":1,"op":"restart","operationId":"0b7c4a1e-3f2d-4c5b-9a8e-123456789abc","code":"busy","message":"another operation"}`, 0, false, CodeBusy},
		"malformed":          {`sudo: a password is required`, 1, true, ""},
		"another request":    {`{"version":1,"op":"stop","operationId":"0b7c4a1e-3f2d-4c5b-9a8e-123456789abc","state":"succeeded"}`, 0, true, ""},
		"another operation":  {`{"version":1,"op":"restart","operationId":"1b7c4a1e-3f2d-4c5b-9a8e-123456789abc","state":"succeeded"}`, 0, true, ""},
		"unknown code":       {`{"version":1,"op":"restart","operationId":"0b7c4a1e-3f2d-4c5b-9a8e-123456789abc","code":"oops"}`, 0, true, ""},
		"unknown field":      {`{"version":1,"op":"restart","operationId":"0b7c4a1e-3f2d-4c5b-9a8e-123456789abc","extra":1}`, 0, true, ""},
		"exit after a reply": {ok, 1, true, ""},
	} {
		t.Run(name, func(t *testing.T) {
			f := (&execx.Fake{}).Reply("lcm-deploy", tc.out, tc.code)
			_, err := Client{Runner: f}.Call(context.Background(), req)
			if errors.Is(err, ErrOutcomeUnknown) != tc.unknown || CodeOf(err) != tc.code2 {
				t.Fatalf("err = %v", err)
			}
		})
	}
	t.Run("truncated", func(t *testing.T) {
		f := (&execx.Fake{}).On(func(execx.Cmd) bool { return true }, func(execx.Cmd) (execx.Result, error) {
			return execx.Result{Output: ok, Truncated: true}, nil
		})
		if _, err := (Client{Runner: f}).Call(context.Background(), req); !errors.Is(err, ErrOutcomeUnknown) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("query", func(t *testing.T) {
		q := Request{Version: Version, Op: OpQuery, OperationID: req.OperationID}
		failed := `{"version":1,"op":"build","operationId":"0b7c4a1e-3f2d-4c5b-9a8e-123456789abc","state":"failed","code":"failed","message":"the build exited with status 1"}`
		resp, err := Client{Runner: (&execx.Fake{}).Reply("lcm-deploy", failed, 0)}.Call(context.Background(), q)
		if err != nil || resp.Op != OpBuild || resp.State != StateFailed {
			t.Fatalf("recorded failure: %+v %v", resp, err)
		}
		missing := `{"version":1,"op":"query","operationId":"0b7c4a1e-3f2d-4c5b-9a8e-123456789abc","code":"not_found","message":"no operation"}`
		if _, err := (Client{Runner: (&execx.Fake{}).Reply("lcm-deploy", missing, 0)}).Call(context.Background(), q); CodeOf(err) != CodeNotFound {
			t.Fatalf("missing: %v", err)
		}
	})
	t.Run("invalid request", func(t *testing.T) {
		f := &execx.Fake{}
		bad := req
		bad.Service = "LCM"
		if _, err := (Client{Runner: f}).Call(context.Background(), bad); err == nil || len(f.Calls()) != 0 {
			t.Fatalf("err = %v, calls %d", err, len(f.Calls()))
		}
	})
}

func TestFake(t *testing.T) {
	f := &Fake{}
	resp, err := f.Call(context.Background(), clientReq())
	if err != nil || resp.State != StateSucceeded || len(f.Requests()) != 1 {
		t.Fatalf("%+v %v", resp, err)
	}
	if _, err := f.Call(context.Background(), Request{Version: Version, Op: OpQuery}); err == nil {
		t.Error("the fake accepted an invalid request")
	}
	f.Handle = func(r Request) (Response, error) {
		return Response{Version: Version, Op: r.Op, OperationID: r.OperationID, Code: CodeRefused}, &Error{Code: CodeRefused}
	}
	if _, err := f.Call(context.Background(), clientReq()); CodeOf(err) != CodeRefused {
		t.Errorf("err = %v", err)
	}
	var _ Caller = Client{}
	var _ Caller = f
}
