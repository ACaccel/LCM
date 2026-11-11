package runtime_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ACaccel/lcm/internal/deployhelper"
	"github.com/ACaccel/lcm/internal/execx"
	rt "github.com/ACaccel/lcm/internal/runtime"
)

// clientHelper is the real client over a fake sudo.
type clientHelper struct{ deployhelper.Client }

func (clientHelper) Ready() error { return nil }

// The reply of a real older helper, which names no op or ID, reaches the
// operator as a refusal with the hint to install the new helper.
func TestOlderHelperReplyThroughTheClient(t *testing.T) {
	const old = `{"version":1,"op":"","operationId":"","code":"refused","message":"json: unknown field \"instances\""}`
	h := clientHelper{deployhelper.Client{Runner: (&execx.Fake{}).Reply("lcm-deploy", old+"\n", 0)}}
	_, err := rt.CallHelper(context.Background(), h, deployhelper.Request{Op: deployhelper.OpActivate, OperationID: rt.NewOperationID(), Service: "web",
		ReleaseID: "20261101T120000Z-0000abcd", Instances: []deployhelper.InstanceSpec{{Name: "a", Start: []string{"x.js"}}}})
	var he *rt.HelperError
	if !errors.As(err, &he) || he.Unknown || he.Code != deployhelper.CodeRefused ||
		!strings.Contains(he.Message, `unknown field "instances"; the pinned deploy helper is older than this LCM: run sudo bin/lcm host apply`) {
		t.Fatalf("err = %v", err)
	}
}
