package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/ACaccel/lcm/internal/model"
)

const maxPromBody = 1 << 20

type promProps struct {
	URL       string   `json:"url"`
	Query     string   `json:"query"`
	Op        string   `json:"op"`
	Threshold *float64 `json:"threshold"`
}

var promOps = map[string]func(v, t float64) bool{
	">":  func(v, t float64) bool { return v > t },
	">=": func(v, t float64) bool { return v >= t },
	"<":  func(v, t float64) bool { return v < t },
	"<=": func(v, t float64) bool { return v <= t },
	"==": func(v, t float64) bool { return v == t },
	"!=": func(v, t float64) bool { return v != t },
}

type promResponse struct {
	Status    string `json:"status"`
	ErrorType string `json:"errorType"`
	Error     string `json:"error"`
	Data      struct {
		ResultType string          `json:"resultType"`
		Result     json.RawMessage `json:"result"`
	} `json:"data"`
}

type promSample struct {
	Value [2]json.RawMessage `json:"value"`
}

func buildPrometheus(_ model.Check, p promProps, _ Env) (Built, error) {
	base, err := url.Parse(p.URL)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		return Built{}, propError("url", "must be the absolute http(s) URL of the Prometheus server")
	}
	if p.Query == "" {
		return Built{}, propError("query", "is required")
	}
	cmp, ok := promOps[p.Op]
	if !ok {
		return Built{}, propError("op", "must be one of >, >=, <, <=, ==, !=")
	}
	if p.Threshold == nil {
		return Built{}, propError("threshold", "is required")
	}
	threshold := *p.Threshold
	endpoint := base.JoinPath("/api/v1/query")
	endpoint.RawQuery = url.Values{"query": {p.Query}}.Encode()
	target := endpoint.String()

	client := &http.Client{Transport: newTransport()}

	return Built{Probe: func(ctx context.Context) Result {
		start := time.Now()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return failure(ctx, err)
		}
		resp, err := client.Do(req)
		if err != nil {
			return failure(ctx, err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxPromBody))
		latency := time.Since(start)
		if err != nil {
			return failure(ctx, err)
		}
		var pr promResponse
		if err := json.Unmarshal(body, &pr); err != nil {
			return Result{Latency: latency, Message: fmt.Sprintf("unexpected response (HTTP %d)", resp.StatusCode)}
		}
		if pr.Status != "success" {
			return Result{Latency: latency, Message: truncate("query failed: "+pr.Error, maxMessage)}
		}
		v, err := singleValue(pr)
		if err != nil {
			return Result{Latency: latency, Message: err.Error()}
		}
		text := strconv.FormatFloat(v, 'g', -1, 64)
		if !cmp(v, threshold) {
			return Result{Latency: latency, Message: fmt.Sprintf("value %s does not satisfy %s %s", text, p.Op, strconv.FormatFloat(threshold, 'g', -1, 64))}
		}
		return Result{OK: true, Latency: latency, Message: "value " + text}
	}}, nil
}

// singleValue extracts the one sample of an instant query. A scalar result
// is also accepted.
func singleValue(pr promResponse) (float64, error) {
	var raw [2]json.RawMessage
	switch pr.Data.ResultType {
	case "scalar":
		if err := json.Unmarshal(pr.Data.Result, &raw); err != nil {
			return 0, fmt.Errorf("malformed scalar result")
		}
	case "vector":
		var samples []promSample
		if err := json.Unmarshal(pr.Data.Result, &samples); err != nil {
			return 0, fmt.Errorf("malformed vector result")
		}
		if len(samples) != 1 {
			return 0, fmt.Errorf("query returned %d samples, want exactly 1", len(samples))
		}
		raw = samples[0].Value
	default:
		return 0, fmt.Errorf("unsupported result type %q", pr.Data.ResultType)
	}
	var text string
	if err := json.Unmarshal(raw[1], &text); err != nil {
		return 0, fmt.Errorf("malformed sample value")
	}
	v, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return 0, fmt.Errorf("sample value %q is not a number", text)
	}
	return v, nil
}
