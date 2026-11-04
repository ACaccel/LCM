// Package xiaomi is a client for the local web API of Xiaomi routers running
// stock firmware (LuCI with the xqsystem/misystem API).
//
// The client only ever calls a fixed allowlist of endpoints: other endpoints
// of the same API (pppoe_status, misystem/newstatus, wifi_detail_all, ...)
// return WAN or Wi-Fi passwords and must never be fetched. The router
// password and the session token never appear in errors or formatted output,
// and the device serial number reported by init_info is never decoded.
package xiaomi

import (
	"context"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // the router's login protocol mandates SHA-1 in its legacy mode
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"math/big"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ACaccel/lcm/internal/clock"
)

// Errors returned by Client methods. Errors wrap one of them so callers can
// classify failures with errors.Is.
var (
	// ErrAuth means the router rejected the credentials. A call never retries
	// a rejected login by itself, so a wrong password costs one attempt.
	ErrAuth = errors.New("xiaomi: authentication rejected")
	// ErrUnavailable means the router could not be reached or did not answer
	// with a usable response.
	ErrUnavailable = errors.New("xiaomi: router unavailable")
	// ErrUnreachable accompanies ErrUnavailable when no HTTP answer arrived
	// at all (connection refused or dropped, timeout), as opposed to an
	// answer that could not be used.
	ErrUnreachable = errors.New("xiaomi: router unreachable")
	// ErrRejected means the router answered a command with an error code, so
	// the command was certainly not performed.
	ErrRejected = errors.New("xiaomi: command rejected")
)

const (
	// DefaultUsername is the only account of the stock web interface.
	DefaultUsername = "admin"
	// DefaultDeviceID identifies this client in login nonces. It is a fixed
	// locally administered MAC address (02:...) so it never collides with a
	// real interface; the router only uses it to keep nonces monotonic.
	DefaultDeviceID = "02:4c:43:4d:00:01"
	// DefaultTimeout bounds a single HTTP request.
	DefaultTimeout = 10 * time.Second

	// passwordKey is the public constant salt of the stock login protocol.
	passwordKey = "a2ffa5c9be07488bbb04a3a47d3c5f6a" //nolint:gosec // public protocol constant shared by all stock firmware, not a credential (gitleaks:allow)
	// maxBody bounds a response body; real API answers are a few KiB.
	maxBody = 1 << 20
)

// endpoint is one API call the client is allowed to make. Every request is
// built from an endpoint value and checked against allowedEndpoints, so a
// new call cannot be added without extending the allowlist deliberately.
type endpoint struct {
	name   string // module/fn, used in errors and the allowlist
	path   string // path below /cgi-bin/luci
	query  string
	authed bool
}

var (
	epInitInfo = endpoint{name: "xqsystem/init_info", path: "/api/xqsystem/init_info"}
	epLogin    = endpoint{name: "xqsystem/login", path: "/api/xqsystem/login"}
	epStatus   = endpoint{name: "misystem/status", path: "/api/misystem/status", authed: true}
	epWAN      = endpoint{name: "xqnetwork/wan_info", path: "/api/xqnetwork/wan_info", authed: true}
	epReboot   = endpoint{name: "xqsystem/reboot", path: "/api/xqsystem/reboot", query: "client=web", authed: true}
	epLogout   = endpoint{name: "web/logout", path: "/web/logout", authed: true}
)

var allowedEndpoints = map[endpoint]bool{
	epInitInfo: true,
	epLogin:    true,
	epStatus:   true,
	epWAN:      true,
	epReboot:   true,
	epLogout:   true,
}

var (
	deviceIDRe = regexp.MustCompile(`^[0-9A-Fa-f]{2}(:[0-9A-Fa-f]{2}){5}$`)
	// tokenRe keeps a token from injecting path segments into later URLs.
	tokenRe = regexp.MustCompile(`^[A-Za-z0-9]{8,128}$`)
)

// HashMode is the digest the router uses in its login protocol.
type HashMode int

// Hash modes reported by init_info's newEncryptMode.
const (
	HashSHA1 HashMode = iota
	HashSHA256
)

func (m HashMode) String() string {
	if m == HashSHA256 {
		return "sha256"
	}
	return "sha1"
}

func (m HashMode) newHash() hash.Hash {
	if m == HashSHA256 {
		return sha256.New()
	}
	return sha1.New() //nolint:gosec // mandated by the router's legacy login mode
}

// Config configures a Client.
type Config struct {
	// BaseURL is the router's web interface, http or https, without a path.
	BaseURL string
	// Username defaults to DefaultUsername.
	Username string
	// Password is the router's web password. It is never formatted or
	// marshaled: Config implements fmt.Formatter and the field is skipped by
	// encoding/json.
	Password string `json:"-"`
	// DeviceID is a MAC-like identifier used in login nonces; defaults to
	// DefaultDeviceID.
	DeviceID string
	// InsecureSkipVerify disables TLS certificate verification for routers
	// with a self-signed certificate. Ignored when HTTP is set.
	InsecureSkipVerify bool
	// Timeout bounds each HTTP request; defaults to DefaultTimeout. Ignored
	// when HTTP is set.
	Timeout time.Duration
	// Clock drives nonce timestamps and login pacing; defaults to the real
	// clock.
	Clock clock.Clock `json:"-"`
	// HTTP replaces the HTTP client, for tests. Redirects are never followed
	// regardless of its CheckRedirect.
	HTTP *http.Client `json:"-"`
}

// Format implements fmt.Formatter so that no verb prints the password.
func (c Config) Format(f fmt.State, _ rune) {
	_, _ = fmt.Fprintf(f, "xiaomi.Config{BaseURL:%q, Username:%q, Password:[redacted], DeviceID:%q, InsecureSkipVerify:%t, Timeout:%s}",
		c.BaseURL, c.Username, c.DeviceID, c.InsecureSkipVerify, c.Timeout)
}

// redacted holds a credential and prints as a placeholder in every verb.
type redacted string

func (redacted) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, "[redacted]") }

// credentials sits behind a pointer in Client: fmt does not call Format on
// unexported fields, but it prints nested pointers only as addresses, so
// even formatting a dereferenced Client cannot reveal them.
type credentials struct {
	password redacted
	// token is the current session token, empty when logged out; guarded
	// by Client.mu.
	token redacted
}

// Client talks to one router. It is safe for concurrent use: logins are
// serialized and the session token is shared by all calls.
type Client struct {
	base     string
	username string
	cred     *credentials
	deviceID string
	clk      clock.Clock
	http     *http.Client
	// once is used for reboot so that a dropped connection is never replayed
	// by the transport (see newOnceClient).
	once *http.Client

	// mu serializes logins and guards the fields below. It is held across
	// the login round trip so that concurrent calls wait for one login
	// instead of each starting their own.
	mu sync.Mutex
	// lastNonce is the Unix second of the latest login nonce; the router
	// rejects a nonce that is not strictly newer for the same device ID.
	lastNonce int64
	// info is the latest init_info answer, used as a fallback for model and
	// firmware.
	info *Info
}

// Format implements fmt.Formatter so that no verb prints credentials.
func (c *Client) Format(f fmt.State, _ rune) {
	_, _ = fmt.Fprintf(f, "xiaomi.Client{BaseURL:%q}", c.base)
}

// New validates cfg and returns a client. It performs no network I/O.
func New(cfg Config) (*Client, error) {
	base, err := parseBaseURL(cfg.BaseURL)
	if err != nil {
		return nil, err
	}
	if cfg.Password == "" {
		return nil, errors.New("xiaomi: password is required")
	}
	username := cfg.Username
	if username == "" {
		username = DefaultUsername
	}
	if strings.ContainsAny(username, "\r\n\x00") {
		return nil, errors.New("xiaomi: invalid username")
	}
	deviceID := cfg.DeviceID
	if deviceID == "" {
		deviceID = DefaultDeviceID
	}
	if !deviceIDRe.MatchString(deviceID) {
		return nil, errors.New("xiaomi: device ID must be a MAC address such as 02:00:00:00:00:01")
	}
	if cfg.Timeout < 0 {
		return nil, errors.New("xiaomi: timeout must not be negative")
	}
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	clk := cfg.Clock
	if clk == nil {
		clk = clock.Real()
	}

	var hc http.Client
	if cfg.HTTP != nil {
		hc = *cfg.HTTP
	} else {
		tr := http.DefaultTransport.(*http.Transport).Clone()
		// The router is on the local network: an environment proxy would
		// only add a party that sees the session token.
		tr.Proxy = nil
		tr.TLSClientConfig = &tls.Config{
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: cfg.InsecureSkipVerify, //nolint:gosec // explicit opt-in for routers with self-signed certificates
		}
		hc = http.Client{Transport: tr, Timeout: timeout}
	}
	// A redirect would carry the token in the URL to another location.
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	return &Client{
		base:     base,
		username: username,
		cred:     &credentials{password: redacted(cfg.Password)},
		deviceID: deviceID,
		clk:      clk,
		http:     &hc,
		once:     newOnceClient(&hc),
	}, nil
}

func parseBaseURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", errors.New("xiaomi: invalid base URL")
	}
	switch {
	case u.Scheme != "http" && u.Scheme != "https":
		return "", errors.New("xiaomi: base URL must use http or https")
	case u.Host == "" || u.Hostname() == "":
		return "", errors.New("xiaomi: base URL has no host")
	case u.User != nil:
		return "", errors.New("xiaomi: base URL must not contain credentials")
	case u.Path != "" && u.Path != "/", u.RawQuery != "", u.Fragment != "", u.Opaque != "":
		return "", errors.New("xiaomi: base URL must not have a path, query or fragment")
	}
	return u.Scheme + "://" + u.Host, nil
}

// newOnceClient returns a client whose requests always use a new connection.
// net/http silently replays an idempotent GET when a reused keep-alive
// connection fails, which for reboot would turn one submission into two and
// hide that the first one was delivered. A fresh connection is never
// replayed. Transports other than *http.Transport are used as they are.
func newOnceClient(base *http.Client) *http.Client {
	c := *base
	var tr *http.Transport
	switch t := base.Transport.(type) {
	case nil:
		tr = http.DefaultTransport.(*http.Transport).Clone()
		tr.Proxy = nil
	case *http.Transport:
		tr = t.Clone()
	default:
		return &c
	}
	tr.DisableKeepAlives = true
	c.Transport = tr
	return &c
}

// response is the envelope every JSON answer shares.
type response struct {
	Code flexNum `json:"code"`
	Msg  string  `json:"msg"`
}

// tokenInvalid reports whether the router refused the session token.
func (r response) tokenInvalid() bool {
	code, ok := r.Code.int()
	return (ok && code == 401) || strings.Contains(strings.ToLower(r.Msg), "invalid token")
}

// result is a completed HTTP exchange.
type result struct {
	status int
	body   []byte
}

// sendError is a transport failure. written records whether the request had
// been fully written when it happened, which decides whether a command's
// outcome is unknown or certainly failed.
type sendError struct {
	ep      endpoint
	written bool
	err     error
}

func (e *sendError) Error() string {
	return fmt.Sprintf("xiaomi: %s: %v: %v", e.ep.name, ErrUnavailable, e.err)
}

func (e *sendError) Unwrap() []error { return []error{ErrUnavailable, ErrUnreachable, e.err} }

// do performs one request. The URL is built here only, from an allowlisted
// endpoint, so a token can only ever be sent to the configured router.
func (c *Client) do(ctx context.Context, hc *http.Client, ep endpoint, token redacted, form url.Values) (result, error) {
	if !allowedEndpoints[ep] {
		return result{}, fmt.Errorf("xiaomi: endpoint %q is not allowed", ep.name)
	}
	u := c.base + "/cgi-bin/luci"
	if ep.authed {
		if !tokenRe.MatchString(string(token)) {
			return result{}, fmt.Errorf("xiaomi: %s: no valid session token", ep.name)
		}
		u += "/;stok=" + string(token)
	}
	u += ep.path
	if ep.query != "" {
		u += "?" + ep.query
	}

	method, body := http.MethodGet, io.Reader(nil)
	if form != nil {
		method, body = http.MethodPost, strings.NewReader(form.Encode())
	}
	var mu sync.Mutex
	written := false
	// Sticky: once any attempt has been delivered, a later failure cannot
	// make the outcome certain again.
	trace := &httptrace.ClientTrace{WroteRequest: func(info httptrace.WroteRequestInfo) {
		if info.Err == nil {
			mu.Lock()
			written = true
			mu.Unlock()
		}
	}}
	req, err := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, trace), method, u, body)
	if err != nil {
		// The URL holds the token; report only the endpoint.
		return result{}, fmt.Errorf("xiaomi: %s: cannot build request", ep.name)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	req.Header.Set("Accept", "application/json")

	resp, err := hc.Do(req)
	wasWritten := func() bool {
		mu.Lock()
		defer mu.Unlock()
		return written
	}
	if err != nil {
		return result{}, &sendError{ep: ep, written: wasWritten(), err: stripURL(err)}
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return result{}, &sendError{ep: ep, written: true, err: stripURL(err)}
	}
	return result{status: resp.StatusCode, body: data}, nil
}

// stripURL removes the request URL, which contains the session token, from
// errors returned by http.Client.
func stripURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

// decode parses a JSON answer into v and its envelope. It returns a short
// reason, safe to show to users, when the answer is unusable.
func decode(res result, v any) (response, string) {
	var env response
	if res.status != http.StatusOK {
		return env, "http " + strconv.Itoa(res.status)
	}
	if !lenientUnmarshal(res.body, &env) {
		return env, "invalid json"
	}
	if v != nil && !lenientUnmarshal(res.body, v) {
		return env, "invalid json"
	}
	return env, ""
}

// lenientUnmarshal decodes data into v and reports whether data was a JSON
// object. A field of an unexpected type is left at its zero value instead
// of failing the whole answer: firmware versions differ in such details.
func lenientUnmarshal(data []byte, v any) bool {
	err := json.Unmarshal(data, v)
	if _, ok := errors.AsType[*json.UnmarshalTypeError](err); ok {
		return isObject(data)
	}
	return err == nil
}

func isObject(data []byte) bool {
	s := strings.TrimSpace(string(data))
	return strings.HasPrefix(s, "{")
}

// Info is the router's unauthenticated self-description. The serial number
// reported by the router is deliberately not part of it.
type Info struct {
	Hardware    string
	Model       string
	Firmware    string
	Name        string
	DisplayName string
	// Initialized is false on a router that has not completed setup.
	Initialized *bool
	// WANConnected reports the router's own view of its WAN link.
	WANConnected *bool
	HashMode     HashMode
}

// initInfo lists only the fields LCM uses; in particular "id" (the serial
// number) and "routerId" are never decoded.
type initInfo struct {
	Hardware       string  `json:"hardware"`
	Model          string  `json:"model"`
	ROMVersion     string  `json:"romversion"`
	RouterName     string  `json:"routername"`
	DisplayName    string  `json:"displayName"`
	Inited         flexNum `json:"inited"`
	Connect        flexNum `json:"connect"`
	NewEncryptMode flexNum `json:"newEncryptMode"`
}

// Info fetches init_info. It needs no login.
func (c *Client) Info(ctx context.Context) (Info, error) {
	info, err := c.fetchInfo(ctx)
	if err != nil {
		return Info{}, err
	}
	c.mu.Lock()
	c.info = &info
	c.mu.Unlock()
	return info, nil
}

func (c *Client) fetchInfo(ctx context.Context) (Info, error) {
	res, err := c.do(ctx, c.http, epInitInfo, "", nil)
	if err != nil {
		return Info{}, err
	}
	var raw initInfo
	env, reason := decode(res, &raw)
	if reason == "" {
		if code, ok := env.Code.int(); ok && code != 0 {
			reason = "code " + strconv.FormatInt(code, 10)
		}
	}
	if reason != "" {
		return Info{}, fmt.Errorf("xiaomi: %s: %w: %s", epInitInfo.name, ErrUnavailable, reason)
	}
	info := Info{
		Hardware:     raw.Hardware,
		Model:        raw.Model,
		Firmware:     raw.ROMVersion,
		Name:         raw.RouterName,
		DisplayName:  raw.DisplayName,
		Initialized:  raw.Inited.flag(),
		WANConnected: raw.Connect.flag(),
	}
	if mode, ok := raw.NewEncryptMode.int(); ok && mode == 1 {
		info.HashMode = HashSHA256
	}
	return info, nil
}

// passwordDigest is the login protocol's password proof:
// hex(H(nonce + hex(H(password + key)))).
func passwordDigest(mode HashMode, password, nonce string) string {
	inner := mode.newHash()
	inner.Write([]byte(password + passwordKey))
	outer := mode.newHash()
	outer.Write([]byte(nonce + hex.EncodeToString(inner.Sum(nil))))
	return hex.EncodeToString(outer.Sum(nil))
}

// session returns the current token, logging in when there is none.
func (c *Client) session(ctx context.Context) (redacted, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cred.token != "" {
		return c.cred.token, nil
	}
	tok, err := c.loginLocked(ctx)
	if err != nil {
		return "", err
	}
	c.cred.token = tok
	return tok, nil
}

// dropToken forgets tok if it is still current, so that a concurrent call
// that already logged in again keeps its new token.
func (c *Client) dropToken(tok redacted) {
	c.mu.Lock()
	if c.cred.token == tok {
		c.cred.token = ""
	}
	c.mu.Unlock()
}

type loginResponse struct {
	Token string `json:"token"`
}

// loginLocked performs one login; c.mu must be held. The hash mode is read
// from init_info on every login because a firmware upgrade can switch it.
func (c *Client) loginLocked(ctx context.Context) (redacted, error) {
	info, err := c.fetchInfo(ctx)
	if err != nil {
		return "", err
	}
	c.info = &info

	ts, err := c.nextNonceSecondLocked(ctx)
	if err != nil {
		return "", err
	}
	n, err := rand.Int(rand.Reader, big.NewInt(10000))
	if err != nil {
		return "", fmt.Errorf("xiaomi: nonce: %w", err)
	}
	nonce := fmt.Sprintf("0_%s_%d_%d", c.deviceID, ts, n.Int64())
	form := url.Values{
		"username": {c.username},
		"password": {passwordDigest(info.HashMode, string(c.cred.password), nonce)},
		"logtype":  {"2"},
		"nonce":    {nonce},
	}
	res, err := c.do(ctx, c.http, epLogin, "", form)
	if err != nil {
		return "", err
	}
	var lr loginResponse
	env, reason := decode(res, &lr)
	if reason != "" {
		return "", fmt.Errorf("xiaomi: %s: %w: %s", epLogin.name, ErrUnavailable, reason)
	}
	code, ok := env.Code.int()
	switch {
	case ok && code == 401:
		return "", fmt.Errorf("xiaomi: %s: %w", epLogin.name, ErrAuth)
	case !ok || code != 0:
		// 1582 (invalid nonce) and similar codes are protocol problems, not
		// wrong credentials.
		return "", fmt.Errorf("xiaomi: %s: %w: code %s", epLogin.name, ErrUnavailable, env.Code.text())
	case !tokenRe.MatchString(lr.Token):
		return "", fmt.Errorf("xiaomi: %s: %w: malformed token", epLogin.name, ErrUnavailable)
	}
	return redacted(lr.Token), nil
}

// nextNonceSecondLocked returns a nonce timestamp strictly greater than the
// previous one, waiting on the clock for the next second when needed: the
// router rejects a login whose nonce is not newer than the last one from the
// same device ID, so at most one login per second can succeed.
func (c *Client) nextNonceSecondLocked(ctx context.Context) (int64, error) {
	now := c.clk.Now()
	if now.Unix() <= c.lastNonce {
		wait := time.Unix(c.lastNonce+1, 0).Sub(now)
		if err := sleep(ctx, c.clk, wait); err != nil {
			return 0, err
		}
		now = c.clk.Now()
	}
	ts := max(now.Unix(), c.lastNonce+1)
	c.lastNonce = ts
	return ts, nil
}

func sleep(ctx context.Context, clk clock.Clock, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	done := make(chan struct{})
	t := clk.AfterFunc(d, func() { close(done) })
	defer t.Stop()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("xiaomi: waiting to log in: %w", context.Cause(ctx))
	}
}

// authed performs an authenticated request, logging in as needed. When the
// router refuses the token it logs in again and retries, at most once per
// call (*relogged tracks this across the requests of one call).
func (c *Client) authed(ctx context.Context, hc *http.Client, ep endpoint, relogged *bool) (result, error) {
	for {
		tok, err := c.session(ctx)
		if err != nil {
			return result{}, err
		}
		res, err := c.do(ctx, hc, ep, tok, nil)
		if err != nil {
			return result{}, err
		}
		var env response
		if res.status == http.StatusOK && json.Unmarshal(res.body, &env) == nil && env.tokenInvalid() && !*relogged {
			*relogged = true
			c.dropToken(tok)
			continue
		}
		return res, nil
	}
}

// Logout ends the session, if any. The local token is forgotten even when
// the router cannot be reached.
func (c *Client) Logout(ctx context.Context) error {
	c.mu.Lock()
	tok := c.cred.token
	c.cred.token = ""
	c.mu.Unlock()
	if tok == "" {
		return nil
	}
	// The answer is an HTML page; only reachability matters.
	_, err := c.do(ctx, c.http, epLogout, tok, nil)
	return err
}

// RebootOutcome classifies a reboot submission.
type RebootOutcome int

// Reboot outcomes. Only RebootAccepted comes with a nil error.
const (
	// RebootNotSent: the request was not delivered (login failed, router
	// unreachable, context done before sending). The router did not reboot
	// because of this call.
	RebootNotSent RebootOutcome = iota
	// RebootAccepted: the router confirmed the command.
	RebootAccepted
	// RebootRejected: the router answered with an error; the command was not
	// performed. The error wraps ErrRejected.
	RebootRejected
	// RebootUnknown: the request was delivered but no usable answer came
	// back, typically because the router dropped the connection while going
	// down. Whether it reboots must be decided from later observations.
	RebootUnknown
)

func (o RebootOutcome) String() string {
	switch o {
	case RebootAccepted:
		return "accepted"
	case RebootRejected:
		return "rejected"
	case RebootUnknown:
		return "unknown"
	default:
		return "not_sent"
	}
}

// Reboot asks the router to restart. The outcome is meaningful even when the
// error is not nil: it tells whether the command may have been performed.
func (c *Client) Reboot(ctx context.Context) (RebootOutcome, error) {
	relogged := false
	res, err := c.authed(ctx, c.once, epReboot, &relogged)
	if err != nil {
		if se, ok := errors.AsType[*sendError](err); ok && se.written && se.ep == epReboot {
			c.forgetSession()
			return RebootUnknown, err
		}
		return RebootNotSent, err
	}
	if res.status >= 400 && res.status < 500 {
		return RebootRejected, fmt.Errorf("xiaomi: %s: %w: http %d", epReboot.name, ErrRejected, res.status)
	}
	env, reason := decode(res, nil)
	if reason != "" {
		// A server error or an unparsable answer may come from a router that
		// is already shutting down; it is not proof of refusal.
		c.forgetSession()
		return RebootUnknown, fmt.Errorf("xiaomi: %s: %w: %s", epReboot.name, ErrUnavailable, reason)
	}
	if code, ok := env.Code.int(); !ok || code != 0 {
		return RebootRejected, fmt.Errorf("xiaomi: %s: %w: code %s", epReboot.name, ErrRejected, env.Code.text())
	}
	c.forgetSession()
	return RebootAccepted, nil
}

// forgetSession drops the token after a reboot may have started: the router
// loses its sessions when it restarts, and a stale token would only cost a
// failed request later.
func (c *Client) forgetSession() {
	c.mu.Lock()
	c.cred.token = ""
	c.mu.Unlock()
}
