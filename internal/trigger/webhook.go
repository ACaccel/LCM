package trigger

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/ACaccel/lcm/internal/engine"
)

// MaxWebhookBody caps the body accepted by Deliver. It stays well below the
// engine's event limit so that the headers and wrapper still fit.
const MaxWebhookBody = 128 << 10

// Errors returned by Deliver.
var (
	// ErrNoListener means no deployed policy listens on the webhook name.
	ErrNoListener = errors.New("trigger: no policy listens on this webhook")
	// ErrBadSignature means every listener requires a signature the request
	// does not carry or that does not match.
	ErrBadSignature = errors.New("trigger: missing or invalid signature")
	// ErrBodyTooLarge means the body exceeds MaxWebhookBody.
	ErrBodyTooLarge = errors.New("trigger: webhook body too large")
)

// secretHeaders are never copied into the event: they would end up in the
// stored run.
var secretHeaders = map[string]bool{
	"authorization": true, "cookie": true, "proxy-authorization": true, "set-cookie": true,
}

// Deliver starts a run for every deployed trigger.webhook node named name
// whose signature requirement is met, and returns the run ids. A listener
// with hmacSecret needs a valid HMAC-SHA256 of the body in its header;
// listeners without one accept any request, so callers must authenticate
// those some other way.
//
// Event: name, headers (lower-case names, first value) and body (parsed when
// it is JSON, otherwise a string).
func (m *Manager) Deliver(ctx context.Context, name string, headers http.Header, body []byte) ([]string, error) {
	return m.deliver(ctx, name, headers, body, false)
}

// DeliverSigned is Deliver for callers that could not authenticate the
// request otherwise: listeners without an HMAC secret are skipped, so only
// a valid signature starts a run. ErrBadSignature is returned when no
// listener accepted the request, including when none requires a signature.
func (m *Manager) DeliverSigned(ctx context.Context, name string, headers http.Header, body []byte) ([]string, error) {
	return m.deliver(ctx, name, headers, body, true)
}

func (m *Manager) deliver(ctx context.Context, name string, headers http.Header, body []byte, signedOnly bool) ([]string, error) {
	if len(body) > MaxWebhookBody {
		return nil, ErrBodyTooLarge
	}
	type listener struct {
		policy, node string
		props        WebhookProps
	}
	var ls []listener
	for _, plan := range m.cfg.Engine.Plans() {
		for _, ti := range plan.Triggers() {
			if p, ok := ti.Props.(WebhookProps); ok && p.Name == name {
				ls = append(ls, listener{plan.Name(), ti.ID, p})
			}
		}
	}
	if len(ls) == 0 {
		return nil, ErrNoListener
	}

	event := map[string]any{"name": name, "headers": flattenHeaders(headers), "body": decodeBody(body)}
	var (
		ids        []string
		errs       []error
		badSig     int
		authorized int
	)
	for _, l := range ls {
		if signedOnly && l.props.HMACSecret == "" {
			badSig++
			continue
		}
		if l.props.HMACSecret != "" {
			ok, err := m.verifyHMAC(l.props, headers, body)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s/%s: %w", l.policy, l.node, err))
				continue
			}
			if !ok {
				badSig++
				continue
			}
		}
		authorized++
		id, err := m.cfg.Engine.Start(ctx, l.policy, l.node, event, engine.ModeLive)
		switch {
		case err == nil, errors.Is(err, engine.ErrDropped):
			ids = append(ids, id)
		case errors.Is(err, engine.ErrPolicyPaused), errors.Is(err, engine.ErrPolicyNotDeployed):
			m.log.Debug("webhook not started", "policy", l.policy, "reason", err)
		default:
			errs = append(errs, fmt.Errorf("%s/%s: %w", l.policy, l.node, err))
		}
	}
	if authorized == 0 && badSig > 0 && len(errs) == 0 {
		return nil, ErrBadSignature
	}
	return ids, errors.Join(errs...)
}

func (m *Manager) verifyHMAC(p WebhookProps, headers http.Header, body []byte) (bool, error) {
	if m.cfg.Secrets == nil {
		return false, errors.New("no secret source configured")
	}
	key, err := m.cfg.Secrets.Get(p.HMACSecret)
	if err != nil {
		return false, err
	}
	got := strings.TrimPrefix(strings.TrimSpace(headers.Get(p.HMACHeader)), "sha256=")
	sig, err := hex.DecodeString(got)
	if err != nil || len(sig) == 0 {
		return false, nil
	}
	mac := hmac.New(sha256.New, []byte(key.Reveal()))
	mac.Write(body)
	return hmac.Equal(sig, mac.Sum(nil)), nil
}

func flattenHeaders(h http.Header) map[string]any {
	out := make(map[string]any, len(h))
	for k, v := range h {
		lk := strings.ToLower(k)
		if len(v) == 0 || secretHeaders[lk] {
			continue
		}
		out[lk] = v[0]
	}
	return out
}

func decodeBody(body []byte) any {
	if len(body) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(body, &v); err == nil {
		return v
	}
	return string(body)
}
