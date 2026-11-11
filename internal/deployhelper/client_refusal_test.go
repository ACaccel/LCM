package deployhelper

import (
	"context"
	"errors"
	"testing"

	"github.com/ACaccel/lcm/internal/execx"
)

// A helper older than this LCM refuses a field it does not know while it
// decodes the request, before it knows the op or ID: the reply names
// neither. That is a refusal, not an unknown outcome, as nothing ran.
func TestClientOlderHelperRefusal(t *testing.T) {
	const old = `{"version":1,"op":"","operationId":"","code":"refused","message":"json: unknown field \"instances\""}`
	f := (&execx.Fake{}).Reply("lcm-deploy", old+"\n", 0)
	req := clientReq()
	req.Instance = "a"
	_, err := Client{Runner: f}.Call(context.Background(), req)
	var he *Error
	if !errors.As(err, &he) || he.Code != CodeRefused || he.Message != `json: unknown field "instances"` || errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("err = %v", err)
	}

	// Any other reply that does not name the request stays unknown.
	for _, reply := range []string{
		`{"version":1,"op":"","operationId":"","code":"failed","message":"the ledger cannot be used"}`,
		`{"version":1,"op":"stop","operationId":"0b7c4a1e-3f2d-4c5b-9a8e-123456789abc","code":"refused","message":"x"}`,
	} {
		f := (&execx.Fake{}).Reply("lcm-deploy", reply+"\n", 0)
		if _, err := (Client{Runner: f}).Call(context.Background(), clientReq()); !errors.Is(err, ErrOutcomeUnknown) {
			t.Errorf("%s: err = %v", reply, err)
		}
	}
}
