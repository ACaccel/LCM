package xiaomi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestPasswordDigestGolden(t *testing.T) {
	// Expected values computed independently with sha1sum/sha256sum.
	const nonce = "0_02:4c:43:4d:00:01_1700000000_42"
	cases := []struct {
		mode HashMode
		want string
	}{
		{HashSHA1, "fbb5c00cddb6975c8904b408bf51ec30d105c073"},
		{HashSHA256, "5d1dd56611f0d1f04aa4ce606d2ce7e45bf61cd53002134f8663a6c0b0dd445a"},
	}
	for _, tc := range cases {
		if got := passwordDigest(tc.mode, "hunter2", nonce); got != tc.want {
			t.Errorf("%s digest = %s, want %s", tc.mode, got, tc.want)
		}
	}
}

func TestFlexNum(t *testing.T) {
	cases := []struct {
		in      string
		f       float64
		fOK     bool
		i       int64
		iOK     bool
		comment string
	}{
		{`12`, 12, true, 12, true, "number"},
		{`"12"`, 12, true, 12, true, "numeric string"},
		{`" 0.25 "`, 0.25, true, 0, false, "fraction string"},
		{`1.5e3`, 1500, true, 1500, true, "exponent"},
		{`"12345.67"`, 12345.67, true, 0, false, "uptime string"},
		{`-3`, -3, true, -3, true, "negative"},
		{`null`, 0, false, 0, false, "null"},
		{`""`, 0, false, 0, false, "empty string"},
		{`"n/a"`, 0, false, 0, false, "text"},
		{`true`, 0, false, 0, false, "bool"},
		{`{"a":1}`, 0, false, 0, false, "object"},
		{`"NaN"`, 0, false, 0, false, "NaN"},
	}
	for _, tc := range cases {
		var n flexNum
		if err := json.Unmarshal([]byte(tc.in), &n); err != nil {
			t.Fatalf("%s: unmarshal: %v", tc.comment, err)
		}
		f, fOK := n.float()
		i, iOK := n.int()
		if f != tc.f || fOK != tc.fOK || i != tc.i || iOK != tc.iOK {
			t.Errorf("%s (%s): float=(%v,%v) int=(%v,%v), want (%v,%v) (%v,%v)",
				tc.comment, tc.in, f, fOK, i, iOK, tc.f, tc.fOK, tc.i, tc.iOK)
		}
	}
}

func TestEndpointAllowlistEnforced(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer srv.Close()
	c, err := New(Config{BaseURL: srv.URL, Password: "p", HTTP: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	// Endpoints that return passwords must be impossible to call even if
	// code constructs them.
	for _, ep := range []endpoint{
		{name: "xqnetwork/pppoe_status", path: "/api/xqnetwork/pppoe_status", authed: true},
		{name: "misystem/newstatus", path: "/api/misystem/newstatus", authed: true},
		{name: "xqnetwork/wifi_detail_all", path: "/api/xqnetwork/wifi_detail_all", authed: true},
		{name: epStatus.name, path: "/api/misystem/status?x=", authed: true},
	} {
		if _, err := c.do(context.Background(), c.http, ep, "0123456789abcdef", nil); err == nil {
			t.Errorf("%s: request allowed", ep.name)
		}
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("server received %d requests", n)
	}
}

func TestMalformedTokenNotSent(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer srv.Close()
	c, err := New(Config{BaseURL: srv.URL, Password: "p", HTTP: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	for _, tok := range []redacted{"", "abc/../../x", "0123456789abcdef?q"} {
		if _, err := c.do(context.Background(), c.http, epStatus, tok, nil); err == nil {
			t.Errorf("token %q accepted", string(tok))
		}
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("server received %d requests", n)
	}
}
