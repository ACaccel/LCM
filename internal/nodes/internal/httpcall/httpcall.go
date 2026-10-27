// Package httpcall performs the HTTP requests of nodes: bounded in time and
// size, with secrets injected from names, redirects confined to one host and
// errors that never reveal secrets or query strings.
package httpcall

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ACaccel/lcm/internal/clock"
	"github.com/ACaccel/lcm/internal/model"
	"github.com/ACaccel/lcm/internal/secret"
)

// Limits and defaults.
const (
	DefaultTimeout  = 30 * time.Second
	MaxTimeout      = 5 * time.Minute
	DefaultMaxBytes = 64 << 10
	// MaxBytesLimit stays below the engine's per-output cap so that a
	// response never gets replaced by a truncation marker.
	MaxBytesLimit = 192 << 10
	maxRedirects  = 5
)

// Doer is the part of *http.Client that is used.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// NewClient returns a client for node requests. Redirects are followed only
// within the original host, because headers added from secrets would
// otherwise travel to wherever a server sends us.
func NewClient() *http.Client {
	return &http.Client{
		Transport:     http.DefaultTransport,
		CheckRedirect: sameHostRedirect,
	}
}

func sameHostRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return fmt.Errorf("stopped after %d redirects", maxRedirects)
	}
	if req.URL.Host != via[0].URL.Host {
		return http.ErrUseLastResponse
	}
	return nil
}

// Spec describes one request.
type Spec struct {
	Method  string
	URL     string
	Headers map[string]string
	// Body is sent as is when it is a string, and as JSON otherwise.
	Body    any
	Timeout time.Duration
	// BearerSecret and HeaderSecrets name secrets; values are resolved when
	// the request is built and appear nowhere else.
	BearerSecret  string
	HeaderSecrets map[string]string
	// ExpectStatus lists accepted statuses such as "200", "200-299"; the
	// default is 200-299.
	ExpectStatus []string
	MaxBytes     int
	// SecretURL marks a URL that carries a credential (Discord webhook
	// URLs do): errors then name only the host.
	SecretURL bool
}

// Response is the outcome of a request that reached the server.
type Response struct {
	Status    int
	Headers   map[string]any
	Body      any
	Truncated bool
	Duration  time.Duration
	// OK is true when Status matches the expected ranges.
	OK bool
}

// Map returns the response in the form nodes output.
func (r Response) Map() map[string]any {
	return map[string]any{
		"status": int64(r.Status), "ok": r.OK, "headers": r.Headers, "body": r.Body,
		"truncated": r.Truncated, "durationMs": r.Duration.Milliseconds(),
	}
}

// StatusError reports an unexpected status.
type StatusError struct {
	Status int
	URL    string
}

// Error implements error.
func (e *StatusError) Error() string {
	return fmt.Sprintf("unexpected status %d from %s", e.Status, e.URL)
}

// Retryable reports whether trying again could succeed: server errors and
// rate limiting, but not client mistakes.
func (e *StatusError) Retryable() bool {
	return e.Status >= 500 || e.Status == http.StatusTooManyRequests ||
		e.Status == http.StatusRequestTimeout || e.Status == http.StatusTooEarly
}

var errTimeout = errors.New("request timeout")

var (
	headerNameRe = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]{1,100}$")
	rangeRe      = regexp.MustCompile(`^([1-5][0-9]{2})(?:-([1-5][0-9]{2}))?$`)
)

// StatusRange is an inclusive range of HTTP statuses.
type StatusRange struct{ Lo, Hi int }

// ParseRanges parses entries such as "200", "200-299"; nil means 200-299.
func ParseRanges(specs []string) ([]StatusRange, error) {
	if len(specs) == 0 {
		return []StatusRange{{200, 299}}, nil
	}
	out := make([]StatusRange, 0, len(specs))
	for _, s := range specs {
		m := rangeRe.FindStringSubmatch(strings.TrimSpace(s))
		if m == nil {
			return nil, fmt.Errorf("expectStatus %q: want a status such as 204 or a range such as 200-299", s)
		}
		lo, _ := strconv.Atoi(m[1])
		hi := lo
		if m[2] != "" {
			hi, _ = strconv.Atoi(m[2])
		}
		if hi < lo {
			return nil, fmt.Errorf("expectStatus %q: range runs backwards", s)
		}
		out = append(out, StatusRange{lo, hi})
	}
	return out, nil
}

func inRanges(status int, rs []StatusRange) bool {
	for _, r := range rs {
		if status >= r.Lo && status <= r.Hi {
			return true
		}
	}
	return false
}

// Validate checks a Spec without sending it.
func (s Spec) Validate() error {
	u, err := url.Parse(s.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("url: want an absolute http or https URL")
	}
	if s.Timeout < 0 || s.Timeout > MaxTimeout {
		return fmt.Errorf("timeout: must be between 0 and %s", MaxTimeout)
	}
	if s.MaxBytes < 0 || s.MaxBytes > MaxBytesLimit {
		return fmt.Errorf("maxBytes: must be between 0 and %d", MaxBytesLimit)
	}
	for h := range s.Headers {
		if !headerNameRe.MatchString(h) {
			return fmt.Errorf("headers: invalid header name %q", h)
		}
	}
	for h, name := range s.HeaderSecrets {
		if !headerNameRe.MatchString(h) {
			return fmt.Errorf("headerSecrets: invalid header name %q", h)
		}
		if !secret.ValidName(name) {
			return fmt.Errorf("headerSecrets.%s: invalid secret name", h)
		}
	}
	if s.BearerSecret != "" && !secret.ValidName(s.BearerSecret) {
		return errors.New("bearerSecret: invalid secret name")
	}
	if _, err := ParseRanges(s.ExpectStatus); err != nil {
		return err
	}
	return nil
}

// Do sends the request. A non-nil Response with an error means the server
// answered with an unexpected status (a *StatusError); without a Response
// the request did not complete.
func Do(ctx context.Context, clk clock.Clock, c Doer, secrets secret.Source, s Spec) (Response, error) {
	if err := s.Validate(); err != nil {
		return Response{}, err
	}
	label := safeURL(s.URL)
	if s.SecretURL {
		label = hostOf(s.URL)
	}
	ranges, _ := ParseRanges(s.ExpectStatus)
	timeout := s.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	maxBytes := s.MaxBytes
	if maxBytes == 0 {
		maxBytes = DefaultMaxBytes
	}
	method := strings.ToUpper(s.Method)
	if method == "" {
		method = http.MethodGet
	}

	body, contentType, err := encodeBody(s.Body)
	if err != nil {
		return Response{}, err
	}
	ctx, cancel := clock.WithTimeoutCause(ctx, clk, timeout, errTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, s.URL, body)
	if err != nil {
		return Response{}, fmt.Errorf("build request: %w", scrub(err))
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for k, v := range s.Headers {
		req.Header.Set(k, v)
	}
	if err := applySecrets(req, secrets, s); err != nil {
		return Response{}, err
	}

	start := clk.Now()
	resp, err := c.Do(req)
	if err != nil {
		if errors.Is(context.Cause(ctx), errTimeout) {
			return Response{}, fmt.Errorf("%s %s: timed out after %s", method, label, timeout)
		}
		return Response{}, fmt.Errorf("%s %s: %w", method, label, scrub(err))
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, int64(maxBytes)+1))
	if err != nil {
		return Response{}, fmt.Errorf("%s %s: read body: %w", method, label, scrub(err))
	}
	truncated := len(raw) > maxBytes
	if truncated {
		raw = raw[:maxBytes]
	}
	out := Response{
		Status: resp.StatusCode, Headers: flatten(resp.Header), Truncated: truncated,
		Body: decodeBody(raw, resp.Header.Get("Content-Type"), truncated), Duration: clk.Now().Sub(start),
		OK: inRanges(resp.StatusCode, ranges),
	}
	if !out.OK {
		return out, &StatusError{Status: resp.StatusCode, URL: method + " " + label}
	}
	return out, nil
}

func encodeBody(b any) (io.Reader, string, error) {
	switch v := b.(type) {
	case nil:
		return nil, "", nil
	case string:
		return strings.NewReader(v), "", nil
	}
	raw, err := json.Marshal(b)
	if err != nil {
		return nil, "", fmt.Errorf("body: %w", err)
	}
	return bytes.NewReader(raw), "application/json", nil
}

func applySecrets(req *http.Request, secrets secret.Source, s Spec) error {
	if s.BearerSecret == "" && len(s.HeaderSecrets) == 0 {
		return nil
	}
	if secrets == nil {
		return errors.New("secrets are not configured")
	}
	if s.BearerSecret != "" {
		v, err := secrets.Get(s.BearerSecret)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+v.Reveal())
	}
	for h, name := range s.HeaderSecrets {
		v, err := secrets.Get(name)
		if err != nil {
			return err
		}
		req.Header.Set(h, v.Reveal())
	}
	return nil
}

// safeURL drops credentials and the query, which often carries tokens.
func safeURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "(invalid url)"
	}
	return u.Scheme + "://" + u.Host + u.EscapedPath()
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "(invalid url)"
	}
	return u.Scheme + "://" + u.Host
}

// scrub unwraps *url.Error, whose message repeats the full URL.
func scrub(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

func flatten(h http.Header) map[string]any {
	out := make(map[string]any, len(h))
	for k, v := range h {
		lk := strings.ToLower(k)
		if len(v) == 0 || lk == "set-cookie" {
			continue
		}
		out[lk] = v[0]
	}
	return out
}

func decodeBody(raw []byte, contentType string, truncated bool) any {
	if len(raw) == 0 {
		return nil
	}
	if !truncated && isJSON(contentType) {
		if v, err := model.DecodeJSONValue(raw); err == nil {
			return v
		}
	}
	return strings.ToValidUTF8(string(raw), "�")
}

func isJSON(contentType string) bool {
	ct := strings.ToLower(strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0]))
	return ct == "application/json" || strings.HasSuffix(ct, "+json")
}
