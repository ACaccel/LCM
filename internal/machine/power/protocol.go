// Package power implements the restricted local machine power handoff protocol.
// Receipts record handoff evidence only, never proof of reboot or power-off.
package power

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"unicode/utf8"
)

const (
	Version                 = 1
	MaxMessage              = 4096
	MaxLifetimeMillis int64 = 5 * 60 * 1000
)

type Action string

const (
	Reboot           Action = "reboot"
	GracefulShutdown Action = "graceful-shutdown"
)

type Method string

const (
	Prepare Method = "prepare"
	Submit  Method = "submit"
	Query   Method = "query-operation"
)

// Binding is immutable across prepare, submit and query. Times are Unix millis.
// RequestHash is SHA-256 of encoding/json's encoding with RequestHash empty.
// Operation IDs must never be reused, including after expiry or controller restore.
type Binding struct {
	ControllerID string `json:"controllerID"`
	OperationID  string `json:"operationID"`
	RequestHash  string `json:"requestHash"`
	Action       Action `json:"action"`
	BootID       string `json:"bootID"`
	IssuedAt     int64  `json:"issuedAt"`
	ExpiresAt    int64  `json:"expiresAt"`
}

type Request struct {
	Version int     `json:"version"`
	Method  Method  `json:"method"`
	Binding Binding `json:"binding"`
}

type State string

const (
	Accepted      State = "accepted"
	Submitting    State = "submitting"
	Failed        State = "failed"
	Indeterminate State = "indeterminate"
)

type Receipt struct {
	Binding Binding `json:"binding"`
	State   State   `json:"state"`
	// OSRequested means the OS command returned success, not that power changed.
	OSRequested bool   `json:"osRequested"`
	UpdatedAt   int64  `json:"updatedAt"`
	Reason      string `json:"reason,omitempty"`
}

type Response struct {
	Version int      `json:"version"`
	Receipt *Receipt `json:"receipt,omitempty"`
	Code    string   `json:"code,omitempty"`
}

var uuid = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func Hash(b Binding) string {
	b.RequestHash = ""
	raw, _ := json.Marshal(b)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (b Binding) validate() error {
	if !uuid.MatchString(b.ControllerID) || !uuid.MatchString(b.OperationID) || !uuid.MatchString(b.BootID) {
		return errors.New("invalid identity")
	}
	if b.Action != Reboot && b.Action != GracefulShutdown {
		return errors.New("unsupported action")
	}
	if b.IssuedAt <= 0 || b.ExpiresAt <= b.IssuedAt || b.ExpiresAt-b.IssuedAt > MaxLifetimeMillis {
		return errors.New("invalid validity window")
	}
	if b.RequestHash != Hash(b) {
		return errors.New("invalid request hash")
	}
	return nil
}

// decodeStrict also rejects duplicate keys, which encoding/json otherwise accepts.
func decodeStrict(r io.Reader, limit int64, dst any) error {
	raw, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return err
	}
	if !utf8.Valid(raw) {
		return errors.New("invalid UTF-8")
	}
	if int64(len(raw)) > limit {
		return errors.New("message exceeds limit")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	if err := uniqueValue(d); err != nil {
		return err
	}
	if _, err := d.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON")
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	return d.Decode(dst)
}

func uniqueValue(d *json.Decoder) error {
	token, err := d.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			token, err := d.Token()
			if err != nil {
				return err
			}
			key, ok := token.(string)
			if !ok || seen[key] {
				return errors.New("duplicate JSON key")
			}
			seen[key] = true
			if err := uniqueValue(d); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if err := uniqueValue(d); err != nil {
				return err
			}
		}
	default:
		return errors.New("invalid JSON delimiter")
	}
	_, err = d.Token()
	return err
}

func DecodeRequest(r io.Reader) (Request, error) {
	var req Request
	if err := decodeStrict(r, MaxMessage, &req); err != nil {
		return req, err
	}
	if req.Version != Version {
		return req, fmt.Errorf("unsupported protocol version")
	}
	if req.Method != Prepare && req.Method != Submit && req.Method != Query {
		return req, errors.New("unsupported method")
	}
	return req, req.Binding.validate()
}
