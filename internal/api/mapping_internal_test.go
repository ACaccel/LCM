package api

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ACaccel/lcm/internal/deployhelper"
	"github.com/ACaccel/lcm/internal/machine"
	"github.com/ACaccel/lcm/internal/maintenance"
	"github.com/ACaccel/lcm/internal/resource"
	"github.com/ACaccel/lcm/internal/router"
	rt "github.com/ACaccel/lcm/internal/runtime"
)

// The resource type errors reach the API through the shared resource
// errors they wrap; each must keep its status, code and message.
func TestFailMapsResourceTypeErrors(t *testing.T) {
	s := &Server{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	cases := []struct {
		err     error
		status  int
		code    string
		message string
	}{
		{resource.ErrNotFound, http.StatusNotFound, codeNotFound, "not found"},
		{machine.ErrNotFound, http.StatusNotFound, codeNotFound, "not found"},
		{fmt.Errorf("%w: %w", router.ErrNotFound, resource.ErrNotFound), http.StatusNotFound, codeNotFound, "not found: resource: not found"},
		{resource.ErrExists, http.StatusConflict, codeAlreadyExists, "already exists"},
		{resource.ErrConflict, http.StatusConflict, codeVersionConflict, "version conflict"},
		{machine.ErrConflict, http.StatusConflict, codeVersionConflict, "version conflict"},
		{router.ErrConflict, http.StatusConflict, codeVersionConflict, "version conflict"},
		{machine.ErrForbidden, http.StatusForbidden, codeForbidden, "only a direct administrator request is allowed"},
		{router.ErrForbidden, http.StatusForbidden, codeForbidden, "only a direct administrator request is allowed"},
		{resource.ErrPrecondition, http.StatusConflict, codePrecondition, "precondition failed"},
		{fmt.Errorf("%w: busy", machine.ErrPrecondition), http.StatusConflict, codePrecondition, "precondition failed: busy"},
		{router.ErrPrecondition, http.StatusConflict, codePrecondition, "precondition failed"},
		{machine.ErrUnsupported, http.StatusConflict, codePrecondition, "capability unsupported or not configured"},
		{fmt.Errorf("%w: only reboot is supported", router.ErrUnsupported), http.StatusConflict, codePrecondition, "action unsupported: only reboot is supported"},
		{maintenance.ErrActive, http.StatusConflict, codePrecondition, ""},
	}
	for _, c := range cases {
		w := httptest.NewRecorder()
		s.fail(w, httptest.NewRequest(http.MethodGet, "/", nil), c.err, nil)
		var body struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("%v: %v", c.err, err)
		}
		if w.Code != c.status || body.Error.Code != c.code {
			t.Errorf("%v: got %d %s, want %d %s", c.err, w.Code, body.Error.Code, c.status, c.code)
		}
		if c.message != "" && body.Error.Message != c.message {
			t.Errorf("%v: message %q, want %q", c.err, body.Error.Message, c.message)
		}
	}
}

// A deploy helper error keeps its meaning: the operator can fix a refusal,
// a busy helper is retried later, and a failure is the helper's, not LCM's.
func TestFailMapsDeployHelperErrors(t *testing.T) {
	s := &Server{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	op := func(code deployhelper.Code, unknown bool) error {
		he := &rt.HelperError{Op: deployhelper.OpReleases, OperationID: "id", Code: code, Unknown: unknown, Message: "the reason"}
		return fmt.Errorf("list the releases of app: %w", he)
	}
	cases := []struct {
		err     error
		status  int
		code    string
		message string
	}{
		{op(deployhelper.CodeRefused, false), http.StatusConflict, codePrecondition, "deploy helper releases: the reason"},
		{op(deployhelper.CodeNotFound, false), http.StatusConflict, codePrecondition, "deploy helper releases: the reason"},
		{op(deployhelper.CodeBusy, false), http.StatusConflict, codeConflict, "deploy helper releases: the reason"},
		{op(deployhelper.CodeFailed, false), http.StatusBadGateway, codeBadGateway, "deploy helper releases: the reason"},
		{op("", true), http.StatusBadGateway, codeBadGateway, "deploy helper releases: outcome unknown, query id: the reason"},
	}
	for _, c := range cases {
		w := httptest.NewRecorder()
		s.fail(w, httptest.NewRequest(http.MethodGet, "/", nil), c.err, nil)
		var body struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("%v: %v", c.err, err)
		}
		if w.Code != c.status || body.Error.Code != c.code || body.Error.Message != c.message {
			t.Errorf("%v: got %d %s %q, want %d %s %q", c.err, w.Code, body.Error.Code, body.Error.Message, c.status, c.code, c.message)
		}
	}
}
