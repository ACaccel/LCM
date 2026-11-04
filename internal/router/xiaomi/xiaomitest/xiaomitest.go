// Package xiaomitest provides a fake Xiaomi router web API for tests. It
// implements the login protocol independently of package xiaomi (both hash
// modes, nonce monotonicity, token expiry), serves configurable status and
// WAN answers, and simulates reboots whose downtime and uptime reset follow
// an injected clock.
package xiaomitest

import (
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // the fake implements the router's SHA-1 login mode
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"maps"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/clock"
)

// Values embedded in the fake's answers. Tests assert that the client never
// exposes them.
const (
	DefaultPassword = "fake-router-password"
	Serial          = "FAKE-SERIAL-0001"
	RouterID        = "fake-router-id-0001"
	HardwareMAC     = "aa:bb:cc:dd:ee:01"
	WANSecret       = "fake-pppoe-password" //nolint:gosec // synthetic test value
	WANAddress      = "203.0.113.7"
)

const passwordKey = "a2ffa5c9be07488bbb04a3a47d3c5f6a" //nolint:gosec // public protocol constant, not a credential (gitleaks:allow)

// Endpoint names an API call of the fake.
type Endpoint string

// Endpoints the fake implements.
const (
	InitInfo Endpoint = "xqsystem/init_info"
	Login    Endpoint = "xqsystem/login"
	Status   Endpoint = "misystem/status"
	WAN      Endpoint = "xqnetwork/wan_info"
	Reboot   Endpoint = "xqsystem/reboot"
	Logout   Endpoint = "web/logout"
)

// Failure makes an endpoint misbehave.
type Failure int

// Failures.
const (
	FailNone Failure = iota
	FailHTTP404
	FailHTTP500
	// FailNonJSON answers 200 with an HTML page.
	FailNonJSON
	// FailCode answers {"code":1}.
	FailCode
	// FailInvalidToken answers {"code":401,"msg":"Invalid token"} even for a
	// valid token.
	FailInvalidToken
	// FailDrop closes the connection after reading the request.
	FailDrop
)

// RebootBehavior is how the fake answers a reboot request.
type RebootBehavior int

// Reboot behaviors.
const (
	// RebootAccept answers {"code":0} and reboots.
	RebootAccept RebootBehavior = iota
	// RebootAcceptAndDrop reboots and closes the connection without an
	// answer, as a router going down quickly does.
	RebootAcceptAndDrop
	// RebootRefuse answers {"code":1} and does not reboot.
	RebootRefuse
)

// Router is a fake router served by an httptest server.
type Router struct {
	srv *httptest.Server
	clk clock.Clock

	mu         sync.Mutex
	password   string
	sha256     bool
	initBody   map[string]any
	statusBody map[string]any
	wanBody    map[string]any
	tokenTTL   time.Duration
	tokens     map[string]time.Time // token -> expiry (zero: never)
	lastNonce  map[string]int64     // device ID -> Unix second
	failures   map[Endpoint]Failure
	reboot     RebootBehavior
	downtime   time.Duration
	bootAt     time.Time
	reboots    int
	logins     int
	attempts   int
	nonces     []string
	calls      map[Endpoint]int
	paths      []string
}

// New starts a fake router whose uptime is one day at clk's current time.
// It is closed when the test ends.
func New(tb testing.TB, clk clock.Clock) *Router {
	tb.Helper()
	r := &Router{
		clk:       clk,
		password:  DefaultPassword,
		tokens:    map[string]time.Time{},
		lastNonce: map[string]int64{},
		failures:  map[Endpoint]Failure{},
		calls:     map[Endpoint]int{},
		bootAt:    clk.Now().Add(-24 * time.Hour),
	}
	r.srv = httptest.NewServer(http.HandlerFunc(r.serve))
	tb.Cleanup(r.srv.Close)
	return r
}

// URL is the base URL to configure the client with.
func (r *Router) URL() string { return r.srv.URL }

// HTTPClient returns a client for the fake's server.
func (r *Router) HTTPClient() *http.Client { return r.srv.Client() }

// Close stops the server, making the router unreachable.
func (r *Router) Close() { r.srv.Close() }

// SetPassword sets the web password.
func (r *Router) SetPassword(p string) { r.with(func() { r.password = p }) }

// SetSHA256 selects the SHA-256 login mode (newEncryptMode 1) or SHA-1.
func (r *Router) SetSHA256(on bool) { r.with(func() { r.sha256 = on }) }

// SetInitInfoBody replaces the init_info answer; nil restores the default.
// newEncryptMode is added from SetSHA256 unless body has it.
func (r *Router) SetInitInfoBody(body map[string]any) { r.with(func() { r.initBody = body }) }

// SetStatusBody replaces the misystem/status answer; nil restores the
// default. "code" and "upTime" (from the clock) are added unless present.
func (r *Router) SetStatusBody(body map[string]any) { r.with(func() { r.statusBody = body }) }

// SetWANBody replaces the xqnetwork/wan_info answer; nil restores the
// default. "code" and info.uptime (the router uptime) are added unless
// present.
func (r *Router) SetWANBody(body map[string]any) { r.with(func() { r.wanBody = body }) }

// SetTokenTTL makes tokens issued from now on expire after d on the clock;
// 0 means they never expire.
func (r *Router) SetTokenTTL(d time.Duration) { r.with(func() { r.tokenTTL = d }) }

// ExpireTokens invalidates every issued token.
func (r *Router) ExpireTokens() { r.with(func() { clear(r.tokens) }) }

// SetFailure makes ep fail with f; FailNone restores normal behavior.
func (r *Router) SetFailure(ep Endpoint, f Failure) { r.with(func() { r.failures[ep] = f }) }

// SetReboot sets how a reboot request is answered and how long the router
// stays unreachable afterwards; during the downtime every connection is
// closed without an answer, and uptime restarts from zero when it ends.
func (r *Router) SetReboot(b RebootBehavior, downtime time.Duration) {
	r.with(func() { r.reboot, r.downtime = b, downtime })
}

// SetUptime sets the current uptime.
func (r *Router) SetUptime(d time.Duration) { r.with(func() { r.bootAt = r.clk.Now().Add(-d) }) }

// Uptime is the current uptime; negative while the router is down.
func (r *Router) Uptime() time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.clk.Now().Sub(r.bootAt)
}

// Reboots is the number of reboots performed.
func (r *Router) Reboots() int { return get(r, func() int { return r.reboots }) }

// Logins is the number of successful logins.
func (r *Router) Logins() int { return get(r, func() int { return r.logins }) }

// LoginAttempts is the number of login requests, successful or not.
func (r *Router) LoginAttempts() int { return get(r, func() int { return r.attempts }) }

// Nonces returns the nonces of all login requests in order.
func (r *Router) Nonces() []string {
	return get(r, func() []string { return append([]string(nil), r.nonces...) })
}

// Calls is the number of requests to ep, including failed ones.
func (r *Router) Calls(ep Endpoint) int { return get(r, func() int { return r.calls[ep] }) }

// Paths returns every requested path in order, with session tokens replaced
// by "TOKEN".
func (r *Router) Paths() []string {
	return get(r, func() []string { return append([]string(nil), r.paths...) })
}

func (r *Router) with(f func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	f()
}

func get[T any](r *Router, f func() T) T {
	r.mu.Lock()
	defer r.mu.Unlock()
	return f()
}

const (
	publicPrefix = "/cgi-bin/luci/api/"
	tokenPrefix  = "/cgi-bin/luci/;stok=" //nolint:gosec // URL prefix, not a credential
)

func (r *Router) serve(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()

	path := req.URL.Path
	ep, token, ok := route(path)
	logged := path
	if token != "" {
		logged = strings.Replace(path, token, "TOKEN", 1)
	}
	r.paths = append(r.paths, logged)
	if r.clk.Now().Before(r.bootAt) {
		drop(w)
		return
	}
	if !ok {
		http.NotFound(w, req)
		return
	}
	r.calls[ep]++

	switch r.failures[ep] {
	case FailHTTP404:
		http.NotFound(w, req)
		return
	case FailHTTP500:
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	case FailNonJSON:
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, "<html><body>login</body></html>")
		return
	case FailCode:
		writeJSON(w, map[string]any{"code": 1, "msg": "fake failure"})
		return
	case FailInvalidToken:
		writeJSON(w, map[string]any{"code": 401, "msg": "Invalid token"})
		return
	case FailDrop:
		drop(w)
		return
	case FailNone:
	}

	if ep == Status || ep == WAN || ep == Reboot || ep == Logout {
		exp, known := r.tokens[token]
		if !known || (!exp.IsZero() && !r.clk.Now().Before(exp)) {
			delete(r.tokens, token)
			if ep == Logout {
				writeHTML(w)
				return
			}
			writeJSON(w, map[string]any{"code": 401, "msg": "Invalid token"})
			return
		}
	}

	switch ep {
	case InitInfo:
		writeJSON(w, r.initInfoLocked())
	case Login:
		r.loginLocked(w, req)
	case Status:
		writeJSON(w, r.statusLocked())
	case WAN:
		writeJSON(w, r.wanLocked())
	case Reboot:
		r.rebootLocked(w)
	case Logout:
		delete(r.tokens, token)
		writeHTML(w)
	}
}

// route maps a request path to an endpoint and the session token in it.
func route(path string) (Endpoint, string, bool) {
	if rest, ok := strings.CutPrefix(path, publicPrefix); ok {
		switch Endpoint(rest) {
		case InitInfo, Login:
			return Endpoint(rest), "", true
		}
		return "", "", false
	}
	rest, ok := strings.CutPrefix(path, tokenPrefix)
	if !ok {
		return "", "", false
	}
	token, rest, ok := strings.Cut(rest, "/")
	if !ok {
		return "", token, false
	}
	switch {
	case rest == string(Logout):
		return Logout, token, true
	case strings.HasPrefix(rest, "api/"):
		switch ep := Endpoint(strings.TrimPrefix(rest, "api/")); ep {
		case Status, WAN, Reboot:
			return ep, token, true
		}
	}
	return "", token, false
}

func (r *Router) initInfoLocked() map[string]any {
	body := r.initBody
	if body == nil {
		body = map[string]any{
			"code":        0,
			"inited":      1,
			"connect":     1,
			"hardware":    "RB03",
			"model":       "xiaomi.router.rb03",
			"romversion":  "1.0.53",
			"routername":  "Redmi_AX6",
			"displayName": "Redmi Router AX6",
			"id":          Serial,
			"routerId":    RouterID,
			"language":    "en",
		}
	}
	body = maps.Clone(body)
	if _, ok := body["newEncryptMode"]; !ok && r.sha256 {
		body["newEncryptMode"] = 1
	}
	return body
}

func (r *Router) loginLocked(w http.ResponseWriter, req *http.Request) {
	r.attempts++
	if req.Method != http.MethodPost || req.ParseForm() != nil {
		writeJSON(w, map[string]any{"code": 400, "msg": "bad request"})
		return
	}
	nonce := req.PostForm.Get("nonce")
	r.nonces = append(r.nonces, nonce)
	parts := strings.Split(nonce, "_")
	if len(parts) != 4 || parts[0] != "0" {
		writeJSON(w, map[string]any{"code": 1582, "msg": "invalid nonce"})
		return
	}
	ts, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || ts <= r.lastNonce[parts[1]] {
		writeJSON(w, map[string]any{"code": 1582, "msg": "invalid nonce"})
		return
	}
	r.lastNonce[parts[1]] = ts
	if req.PostForm.Get("username") != "admin" || req.PostForm.Get("logtype") != "2" ||
		req.PostForm.Get("password") != r.digest(nonce) {
		writeJSON(w, map[string]any{"code": 401, "msg": "not auth"})
		return
	}
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	token := hex.EncodeToString(b)
	var exp time.Time
	if r.tokenTTL > 0 {
		exp = r.clk.Now().Add(r.tokenTTL)
	}
	r.tokens[token] = exp
	r.logins++
	writeJSON(w, map[string]any{"code": 0, "token": token, "url": "/cgi-bin/luci/;stok=" + token + "/web/home"})
}

func (r *Router) digest(nonce string) string {
	newHash := func() hash.Hash {
		if r.sha256 {
			return sha256.New()
		}
		return sha1.New() //nolint:gosec // the fake implements the router's SHA-1 login mode
	}
	h := newHash()
	h.Write([]byte(r.password + passwordKey))
	inner := hex.EncodeToString(h.Sum(nil))
	h = newHash()
	h.Write([]byte(nonce + inner))
	return hex.EncodeToString(h.Sum(nil))
}

func (r *Router) uptimeSecondsLocked() float64 {
	return r.clk.Now().Sub(r.bootAt).Seconds()
}

func (r *Router) statusLocked() map[string]any {
	body := r.statusBody
	if body == nil {
		body = map[string]any{
			"cpu":         map[string]any{"core": 4, "hz": "1.4GHz", "load": 0.1234},
			"mem":         map[string]any{"usage": 0.45, "total": "512MB", "type": "DDR3"},
			"temperature": 52,
			"count":       map[string]any{"online": 5, "all": 12},
			"wan": map[string]any{
				"upspeed": "1024", "downspeed": "20480",
				"upload": "123456789", "download": "987654321",
				"devname": "eth1",
			},
			"hardware": map[string]any{
				"platform": "RB03", "version": "1.0.53", "channel": "release",
				"sn": Serial, "mac": HardwareMAC,
			},
			"dev": []any{map[string]any{"mac": "aa:bb:cc:dd:ee:99", "devname": "phone"}},
		}
	}
	body = maps.Clone(body)
	if _, ok := body["code"]; !ok {
		body["code"] = 0
	}
	if _, ok := body["upTime"]; !ok {
		body["upTime"] = strconv.FormatFloat(r.uptimeSecondsLocked(), 'f', 2, 64)
	}
	return body
}

func (r *Router) wanLocked() map[string]any {
	body := r.wanBody
	if body == nil {
		body = map[string]any{
			"info": map[string]any{
				"link":   1,
				"status": 1,
				"mac":    HardwareMAC,
				"ipv4":   []any{map[string]any{"ip": WANAddress, "mask": "255.255.255.0"}},
				"details": map[string]any{
					"wanType":  "pppoe",
					"username": "fake-user",
					"password": WANSecret,
				},
			},
		}
	}
	body = maps.Clone(body)
	if _, ok := body["code"]; !ok {
		body["code"] = 0
	}
	if info, ok := body["info"].(map[string]any); ok {
		if _, ok := info["uptime"]; !ok {
			info = maps.Clone(info)
			info["uptime"] = int64(r.uptimeSecondsLocked())
			body["info"] = info
		}
	}
	return body
}

func (r *Router) rebootLocked(w http.ResponseWriter) {
	switch r.reboot {
	case RebootRefuse:
		writeJSON(w, map[string]any{"code": 1, "msg": "refused"})
		return
	case RebootAcceptAndDrop:
		r.restartLocked()
		drop(w)
	case RebootAccept:
		r.restartLocked()
		writeJSON(w, map[string]any{"code": 0})
	}
}

func (r *Router) restartLocked() {
	r.reboots++
	r.bootAt = r.clk.Now().Add(r.downtime)
	clear(r.tokens)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeHTML(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html")
	_, _ = fmt.Fprint(w, "<html><body>bye</body></html>")
}

// drop closes the connection without an answer.
func drop(w http.ResponseWriter) {
	conn, _, err := http.NewResponseController(w).Hijack()
	if err != nil {
		// The server closes the connection without an answer.
		panic(http.ErrAbortHandler)
	}
	_ = conn.Close()
}
