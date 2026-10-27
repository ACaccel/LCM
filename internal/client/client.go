// Package client is a small HTTP client for the LCM API, used by the lcm
// command to talk to a running server.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// maxResponse bounds what is read from a response body.
const maxResponse = 64 << 20

// DefaultTimeout bounds one request. Syncing a source runs git inside the
// request, so it is generous.
const DefaultTimeout = 5 * time.Minute

// Client calls the API of one server with one token.
type Client struct {
	base  string
	token string
	hc    *http.Client
}

// New returns a Client for the server URL, such as http://127.0.0.1:7800.
// The token is sent as a bearer token to that server only: redirects are
// not followed, so it can never be forwarded to another host. hc may be nil.
func New(server, token string, hc *http.Client) (*Client, error) {
	u, err := url.Parse(strings.TrimSpace(server))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("invalid server URL %q: want http://host:port or https://host", server)
	}
	if token == "" {
		return nil, errors.New("client: empty token")
	}
	if hc == nil {
		hc = &http.Client{Timeout: DefaultTimeout}
	}
	c := *hc
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{base: strings.TrimRight(u.String(), "/") + "/api/v1", token: token, hc: &c}, nil
}

// Path joins path segments, escaping each of them.
func Path(parts ...string) string {
	var sb strings.Builder
	for _, p := range parts {
		sb.WriteByte('/')
		sb.WriteString(url.PathEscape(p))
	}
	return sb.String()
}

// Request describes one call.
type Request struct {
	Method string
	// Path starts with "/" and is relative to /api/v1; build it with Path.
	Path        string
	Query       url.Values
	Body        []byte
	ContentType string
}

// Response is a successful (2xx) answer.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
}

// Issue is one validation problem of an error response.
type Issue struct {
	Path    string
	Message string
}

// Error is an error answer of the server.
type Error struct {
	Status  int
	Code    string
	Message string
	Details map[string]any
}

func (e *Error) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("%s (HTTP %d)", e.Message, e.Status)
	}
	return fmt.Sprintf("%s (%s, HTTP %d)", e.Message, e.Code, e.Status)
}

// Issues returns the validation problems of a 422, if any.
func (e *Error) Issues() []Issue {
	list, _ := e.Details["issues"].([]any)
	var out []Issue
	for _, it := range list {
		m, _ := it.(map[string]any)
		path, _ := m["path"].(string)
		msg, _ := m["message"].(string)
		out = append(out, Issue{Path: path, Message: msg})
	}
	return out
}

// Do sends the request. Answers other than 2xx are returned as *Error.
func (c *Client) Do(ctx context.Context, r Request) (*Response, error) {
	target := c.base + r.Path
	if len(r.Query) > 0 {
		target += "?" + r.Query.Encode()
	}
	var body io.Reader
	if r.Body != nil {
		body = bytes.NewReader(r.Body)
	}
	req, err := http.NewRequestWithContext(ctx, r.Method, target, body)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("User-Agent", "lcm-cli")
	if r.ContentType != "" {
		req.Header.Set("Content-Type", r.ContentType)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cannot reach the server: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if len(raw) > maxResponse {
		return nil, fmt.Errorf("response exceeds %d bytes", maxResponse)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, parseError(resp.StatusCode, raw)
	}
	return &Response{Status: resp.StatusCode, Header: resp.Header, Body: raw}, nil
}

func parseError(status int, raw []byte) *Error {
	var env struct {
		Error struct {
			Code    string         `json:"code"`
			Message string         `json:"message"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &env) == nil && env.Error.Message != "" {
		return &Error{Status: status, Code: env.Error.Code, Message: env.Error.Message, Details: env.Error.Details}
	}
	msg := strings.TrimSpace(string(raw))
	if len(msg) > 200 {
		msg = msg[:200] + "..."
	}
	if msg == "" || status >= 300 && status < 400 {
		msg = http.StatusText(status)
		if status >= 300 && status < 400 {
			msg += " (the server redirected; check the server URL)"
		}
	}
	return &Error{Status: status, Message: msg}
}

// JSON sends in (if not nil) as a JSON body and decodes the JSON answer into
// out (if not nil).
func (c *Client) JSON(ctx context.Context, method, path string, query url.Values, in, out any) error {
	req := Request{Method: method, Path: path, Query: query}
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		req.Body, req.ContentType = b, "application/json"
	}
	resp, err := c.Do(ctx, req)
	if err != nil {
		return err
	}
	if out == nil || len(resp.Body) == 0 {
		return nil
	}
	if err := json.Unmarshal(resp.Body, out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}
