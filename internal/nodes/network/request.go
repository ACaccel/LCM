package network

import (
	"context"
	"fmt"
	"strings"

	"github.com/ACaccel/lcm/internal/node"
	"github.com/ACaccel/lcm/internal/nodes/internal/httpcall"
)

// RequestProps configures http.request.
type RequestProps struct {
	Method        string            `json:"method" lcm:"required" enum:"GET,HEAD,POST,PUT,PATCH,DELETE" desc:"HTTP method"`
	URL           string            `json:"url" lcm:"required" desc:"absolute http or https URL"`
	Effect        string            `json:"effect" enum:"read,write" desc:"read lets dry runs send the request; defaults to read for GET and HEAD, write otherwise"`
	Headers       map[string]string `json:"headers" desc:"request headers; do not put secrets here, use bearerSecret or headerSecrets"`
	Body          any               `json:"body" desc:"a string is sent as is, anything else as JSON"`
	Timeout       node.Duration     `json:"timeout" default:"30s" desc:"per-request timeout (at most 5m)"`
	ExpectStatus  []string          `json:"expectStatus" desc:"accepted statuses such as 204 or 200-299 (default 200-299); others fail the node"`
	MaxBytes      int               `json:"maxBytes" default:"65536" desc:"response bytes kept (at most 196608); the rest is cut and truncated is set"`
	BearerSecret  string            `json:"bearerSecret" desc:"secret name sent as a bearer token"`
	HeaderSecrets map[string]string `json:"headerSecrets" desc:"header name to secret name"`
}

// GetProps configures http.get.
type GetProps struct {
	URL           string            `json:"url" lcm:"required" desc:"absolute http or https URL"`
	Headers       map[string]string `json:"headers" desc:"request headers; do not put secrets here, use bearerSecret or headerSecrets"`
	Timeout       node.Duration     `json:"timeout" default:"30s" desc:"per-request timeout (at most 5m)"`
	ExpectStatus  []string          `json:"expectStatus" desc:"accepted statuses such as 204 or 200-299 (default 200-299); others fail the node"`
	MaxBytes      int               `json:"maxBytes" default:"65536" desc:"response bytes kept (at most 196608); the rest is cut and truncated is set"`
	BearerSecret  string            `json:"bearerSecret" desc:"secret name sent as a bearer token"`
	HeaderSecrets map[string]string `json:"headerSecrets" desc:"header name to secret name"`
}

func (p RequestProps) spec() httpcall.Spec {
	return httpcall.Spec{
		Method: p.Method, URL: p.URL, Headers: p.Headers, Body: p.Body, Timeout: p.Timeout.D(),
		BearerSecret: p.BearerSecret, HeaderSecrets: p.HeaderSecrets, ExpectStatus: p.ExpectStatus, MaxBytes: p.MaxBytes,
	}
}

// readOnly reports whether the request may run in a dry run.
func (p RequestProps) readOnly() bool {
	switch p.Effect {
	case "read":
		return true
	case "write":
		return false
	}
	return p.Method == "GET" || p.Method == "HEAD"
}

func validateRequest(p RequestProps) error {
	if p.Method == "GET" || p.Method == "HEAD" {
		if p.Body != nil {
			return fmt.Errorf("body: %s requests do not take a body", p.Method)
		}
	}
	return p.spec().Validate()
}

func registerRequest(r *node.Registry, d Deps) {
	run := func(ctx context.Context, rc *node.RunContext, spec httpcall.Spec) (node.Result, error) {
		// Servers that support it can deduplicate a retried or recovered
		// attempt with the engine's stable key.
		if spec.Method != "GET" && spec.Method != "HEAD" && !hasHeader(spec.Headers, "Idempotency-Key") {
			h := make(map[string]string, len(spec.Headers)+1)
			for k, v := range spec.Headers {
				h[k] = v
			}
			h["Idempotency-Key"] = rc.IdempotencyKey
			spec.Headers = h
		}
		resp, err := httpcall.Do(ctx, rc.Clock, d.client(), d.Secrets, spec)
		if err != nil {
			return node.Result{}, failure(err)
		}
		return node.Result{Output: resp.Map()}, nil
	}

	node.Register(r, node.Spec[RequestProps]{
		Type: "http.request", Category: CategoryNetwork, Effect: node.EffectWrite,
		Description: "Sends an HTTP request. Output: status, ok, headers, body (decoded when JSON), truncated, durationMs. A status outside expectStatus fails the node.",
		Validate:    validateRequest,
		Run: func(ctx context.Context, rc *node.RunContext, p RequestProps) (node.Result, error) {
			return run(ctx, rc, p.spec())
		},
		DryRun: func(ctx context.Context, rc *node.RunContext, p RequestProps) (node.Result, error) {
			if p.readOnly() {
				return run(ctx, rc, p.spec())
			}
			return node.Result{Output: map[string]any{"planned": true, "method": p.Method, "url": p.URL}}, nil
		},
	})
	node.Register(r, node.Spec[GetProps]{
		Type: "http.get", Category: CategoryNetwork, Effect: node.EffectRead,
		Description: "Sends an HTTP GET request; unlike http.request it is a read node, so dry runs and replays record its response.",
		Validate: func(p GetProps) error {
			return httpcall.Spec{Method: "GET", URL: p.URL, Headers: p.Headers, Timeout: p.Timeout.D(), MaxBytes: p.MaxBytes,
				BearerSecret: p.BearerSecret, HeaderSecrets: p.HeaderSecrets, ExpectStatus: p.ExpectStatus}.Validate()
		},
		Run: func(ctx context.Context, rc *node.RunContext, p GetProps) (node.Result, error) {
			return run(ctx, rc, httpcall.Spec{
				Method: "GET", URL: p.URL, Headers: p.Headers, Timeout: p.Timeout.D(), MaxBytes: p.MaxBytes,
				BearerSecret: p.BearerSecret, HeaderSecrets: p.HeaderSecrets, ExpectStatus: p.ExpectStatus,
			})
		},
	})
}

func hasHeader(h map[string]string, name string) bool {
	for k := range h {
		if strings.EqualFold(k, name) {
			return true
		}
	}
	return false
}
