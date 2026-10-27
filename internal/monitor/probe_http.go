package monitor

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/ACaccel/lcm/internal/model"
)

const (
	// maxMatchBody bounds the body read when bodyContains is set.
	maxMatchBody = 1 << 20
	// maxDrainBody bounds how much of an ignored body is read, so that a
	// slow endless response cannot hold a check slot until the timeout.
	maxDrainBody = 64 << 10
)

// errCheckTimeout is the cancellation cause of a run that exceeded the
// check's timeout.
var errCheckTimeout = errors.New("check timed out")

// failure turns a probe error into a failed Result. Network errors are
// reduced to their cause because *url.Error text embeds the full URL, which
// may carry credentials in its query string.
func failure(ctx context.Context, err error) Result {
	if errors.Is(context.Cause(ctx), errCheckTimeout) {
		return Result{Message: "timed out"}
	}
	if ctx.Err() != nil {
		return Result{Message: "canceled"}
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	return Result{Message: truncate(err.Error(), maxMessage)}
}

// newTransport returns a transport for probes. Checks run on an interval,
// so pooled connections would only sit idle, and environment proxies are
// ignored because a health check must reach the target directly.
func newTransport() *http.Transport {
	return &http.Transport{
		DialContext:         (&net.Dialer{}).DialContext,
		TLSHandshakeTimeout: 10 * time.Second,
		DisableKeepAlives:   true,
	}
}

type httpProps struct {
	URL                string            `json:"url"`
	Method             string            `json:"method"`
	ExpectStatus       []int             `json:"expectStatus"`
	BodyContains       string            `json:"bodyContains"`
	Headers            map[string]string `json:"headers"`
	InsecureSkipVerify bool              `json:"insecureSkipVerify"`
}

func buildHTTP(_ model.Check, p httpProps, _ Env) (Built, error) {
	u, err := url.Parse(p.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return Built{}, propError("url", "must be an absolute http or https URL")
	}
	method := strings.ToUpper(p.Method)
	if method == "" {
		method = http.MethodGet
	}
	if !slices.Contains([]string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodOptions}, method) {
		return Built{}, propError("method", "unsupported method %q", p.Method)
	}
	for _, c := range p.ExpectStatus {
		if c < 100 || c > 599 {
			return Built{}, propError("expectStatus", "invalid status code %d", c)
		}
	}
	for k := range p.Headers {
		if k == "" || strings.ContainsAny(k, " \t\r\n:") {
			return Built{}, propError("headers", "invalid header name %q", k)
		}
	}

	tr := newTransport()
	if p.InsecureSkipVerify {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // explicit per-check opt-in for self-signed internal endpoints
	}
	client := &http.Client{
		Transport: tr,
		// A redirect is a valid answer: the default 200-399 range accepts
		// it, and following it would hide what the service itself returns.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	expect := func(code int) bool {
		if len(p.ExpectStatus) == 0 {
			return code >= 200 && code <= 399
		}
		return slices.Contains(p.ExpectStatus, code)
	}
	want := []byte(p.BodyContains)

	return Built{Probe: func(ctx context.Context) Result {
		start := time.Now()
		req, err := http.NewRequestWithContext(ctx, method, p.URL, nil)
		if err != nil {
			return failure(ctx, err)
		}
		for k, v := range p.Headers {
			req.Header.Set(k, v)
		}
		resp, err := client.Do(req)
		if err != nil {
			return failure(ctx, err)
		}
		defer func() { _ = resp.Body.Close() }()
		var body []byte
		if len(want) > 0 {
			body, err = io.ReadAll(io.LimitReader(resp.Body, maxMatchBody))
		} else {
			_, err = io.Copy(io.Discard, io.LimitReader(resp.Body, maxDrainBody))
		}
		latency := time.Since(start)
		if err != nil {
			return failure(ctx, err)
		}
		switch {
		case !expect(resp.StatusCode):
			return Result{Latency: latency, Message: fmt.Sprintf("unexpected status %d", resp.StatusCode)}
		case len(want) > 0 && !bytes.Contains(body, want):
			return Result{Latency: latency, Message: "response body does not contain the expected text"}
		}
		return Result{OK: true, Latency: latency, Message: fmt.Sprintf("status %d", resp.StatusCode)}
	}}, nil
}

type tcpProps struct {
	Address string `json:"address"`
}

func buildTCP(_ model.Check, p tcpProps, _ Env) (Built, error) {
	host, port, err := net.SplitHostPort(p.Address)
	if err != nil || host == "" || port == "" {
		return Built{}, propError("address", "must be host:port")
	}
	return Built{Probe: func(ctx context.Context) Result {
		start := time.Now()
		var d net.Dialer
		conn, err := d.DialContext(ctx, "tcp", p.Address)
		if err != nil {
			return failure(ctx, err)
		}
		_ = conn.Close()
		return Result{OK: true, Latency: time.Since(start)}
	}}, nil
}
