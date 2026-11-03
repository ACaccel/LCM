package securityhost

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/ACaccel/lcm/internal/audit"
	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/clock"
	"github.com/ACaccel/lcm/internal/model"
)

var identifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,99}$`)
var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var actions = []string{"aide.check", "aide.schedule", "nginx.configure", "audit.profile", "aide.profile", "baseline.request"}

// Client persists administrator intent separately from the root helper journal.
type Client struct {
	cfg Config
	mu  sync.Mutex
}

func New(cfg Config) (*Client, error) {
	if cfg.DB == nil || cfg.Audit == nil || !identifier.MatchString(cfg.MachineID) {
		return nil, errors.New("securityhost: DB, Audit and MachineID are required")
	}
	if cfg.Clock == nil {
		cfg.Clock = clock.Real()
	}
	if cfg.Runner == nil {
		cfg.Runner = systemRunner{}
	}
	return &Client{cfg: cfg}, nil
}

func (c *Client) Status(ctx context.Context) (HostStatus, error) {
	reply, err := c.run(ctx, Request{Command: "status"})
	if err != nil || reply.Status == nil || reply.Status.MachineID != c.cfg.MachineID {
		out := HostStatus{MachineID: c.cfg.MachineID, Reason: "Restricted host helper unavailable or incompatible; host controls are read-only.", Capabilities: []Capability{}, AIDE: AIDEStatus{Profiles: []Profile{}}, Audit: AuditStatus{Profiles: []Profile{}}, Nginx: NginxStatus{DryRunOnly: true, Limits: []Limit{}}, Collector: CollectorStatus{Health: map[string]any{}}}
		for _, action := range actions {
			out.Capabilities = append(out.Capabilities, Capability{Action: action, Reason: out.Reason})
		}
		return out, nil
	}
	return *reply.Status, nil
}

func (c *Client) run(ctx context.Context, r Request) (Response, error) {
	r.Version = ProtocolVersion
	r.MachineID = c.cfg.MachineID
	reply, err := c.cfg.Runner.Run(ctx, r)
	if err == nil && reply.Version != ProtocolVersion {
		return Response{}, ErrUnavailable
	}
	return reply, err
}

func validateText(value string) bool {
	return strings.TrimSpace(value) != "" && utf8.RuneCountInString(value) <= 500 && !strings.ContainsFunc(value, unicode.IsControl)
}
func actor(ctx context.Context) (string, error) {
	a, ok := auth.ActorFrom(ctx)
	if !ok || !a.Kind.Valid() {
		return "", audit.ErrNoActor
	}
	return string(a.Kind) + ":" + a.ID, nil
}

func (c *Client) Preview(ctx context.Context, in PreviewRequest) (Preview, error) {
	if err := validatePreview(in); err != nil {
		return Preview{}, err
	}
	who, err := actor(ctx)
	if err != nil {
		return Preview{}, err
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return Preview{}, err
	}
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	c.mu.Lock()
	defer c.mu.Unlock()
	existing, stored, preview, err := c.lookup(ctx, in.RequestID)
	if err == nil {
		if stored != digest || existing.Actor != who {
			return Preview{}, ErrConflict
		}
		return preview, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return Preview{}, err
	}
	var n int
	if err = c.cfg.DB.Reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM security_host_operations`).Scan(&n); err != nil {
		return Preview{}, err
	}
	if n >= 2000 {
		return Preview{}, model.Issues{{Path: "requestId", Message: "operation history capacity reached; local review required"}}
	}
	reply, err := c.run(ctx, Request{Command: "preview", RequestID: in.RequestID, Action: in.Action, Reason: in.Reason, Actor: who, ExpectedDigest: in.ExpectedDigest, Settings: in.Settings})
	if err != nil {
		return Preview{}, err
	}
	if reply.Preview == nil || reply.Preview.RequestID != in.RequestID || reply.Preview.Action != in.Action || !digestPattern.MatchString(reply.Preview.PreviewHash) {
		return Preview{}, ErrUnavailable
	}
	p := *reply.Preview
	now := c.cfg.Clock.Now().UTC()
	op := Operation{ID: in.RequestID, Action: in.Action, State: "prepared", Reason: in.Reason, Actor: who, CreatedAt: now, UpdatedAt: now, PreviewHash: p.PreviewHash, EffectiveDigest: in.ExpectedDigest}
	pb, _ := json.Marshal(p)
	ob, _ := json.Marshal(op)
	err = c.cfg.DB.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO security_host_operations(id,action,actor,request_digest,request_body,preview_body,operation_body,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?)`, op.ID, op.Action, who, digest, string(raw), string(pb), string(ob), now.UnixMilli(), now.UnixMilli())
		return err
	})
	if err != nil {
		return Preview{}, err
	}
	if err = c.cfg.Audit.Record(ctx, "security.host.preview", op.ID, audit.OutcomeOK, map[string]any{"action": op.Action, "digest": op.EffectiveDigest}); err != nil {
		return Preview{}, err
	}
	return p, nil
}

func validatePreview(in PreviewRequest) error {
	var issues model.Issues
	if !identifier.MatchString(in.RequestID) {
		issues = append(issues, model.Issue{Path: "requestId", Message: "use 1 to 100 safe identifier characters"})
	}
	allowed := false
	for _, action := range actions {
		allowed = allowed || action == in.Action
	}
	if !allowed {
		issues = append(issues, model.Issue{Path: "action", Message: "unsupported fixed host action"})
	}
	if !validateText(in.Reason) {
		issues = append(issues, model.Issue{Path: "reason", Message: "use 1 to 500 printable characters"})
	}
	if !digestPattern.MatchString(in.ExpectedDigest) {
		issues = append(issues, model.Issue{Path: "expectedDigest", Message: "refresh effective configuration before preview"})
	}
	if len(in.Settings) == 0 || len(in.Settings) > 8192 || !json.Valid(in.Settings) || string(in.Settings)[0] != '{' {
		issues = append(issues, model.Issue{Path: "settings", Message: "a bounded typed settings object is required"})
	}
	if err := issues.Err(); err != nil {
		return err
	}
	return validateSettings(in.Action, in.Settings)
}

func (c *Client) Apply(ctx context.Context, in ApplyRequest) (Operation, error) {
	if !identifier.MatchString(in.RequestID) || !digestPattern.MatchString(in.PreviewHash) || !validateText(in.Reason) {
		return Operation{}, model.Issues{{Path: "request", Message: "requestId, previewHash and printable reason are required"}}
	}
	who, err := actor(ctx)
	if err != nil {
		return Operation{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	op, _, preview, err := c.lookup(ctx, in.RequestID)
	if err != nil {
		return Operation{}, err
	}
	if op.Actor != who || op.Reason != in.Reason || op.PreviewHash != in.PreviewHash {
		return Operation{}, ErrConflict
	}
	if op.State != "prepared" {
		return c.reconcile(ctx, op)
	}
	if !c.cfg.Clock.Now().Before(preview.ExpiresAt) {
		return Operation{}, ErrConflict
	}
	op.State = "running"
	op.UpdatedAt = c.cfg.Clock.Now().UTC()
	if err = c.save(ctx, op); err != nil {
		return Operation{}, err
	}
	reply, runErr := c.run(ctx, Request{Command: "apply", RequestID: in.RequestID, PreviewHash: in.PreviewHash, Reason: in.Reason, Actor: who})
	if runErr != nil || !validOperation(reply.Operation, op) {
		op.State = "indeterminate"
		op.Error = "The helper result is unknown; query this operation before any retry."
		if errors.Is(runErr, ErrConflict) {
			op.State = "failed"
			op.Error = "The preview expired or effective configuration changed; refresh and create a new preview."
		}
	} else {
		op = mergeOperation(op, *reply.Operation)
	}
	op.UpdatedAt = c.cfg.Clock.Now().UTC()
	if err = c.save(ctx, op); err != nil {
		return Operation{}, err
	}
	outcome := audit.OutcomeOK
	if op.State == "failed" || op.State == "indeterminate" {
		outcome = audit.OutcomeError
	}
	if err = c.cfg.Audit.Record(ctx, "security.host.apply", op.ID, outcome, map[string]any{"action": op.Action, "state": op.State, "reason": in.Reason, "previewHash": in.PreviewHash}); err != nil {
		return op, err
	}
	return op, nil
}

func (c *Client) Operation(ctx context.Context, id string) (Operation, error) {
	if !identifier.MatchString(id) {
		return Operation{}, ErrNotFound
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	op, _, _, err := c.lookup(ctx, id)
	if err != nil {
		return Operation{}, err
	}
	return c.reconcile(ctx, op)
}
func (c *Client) reconcile(ctx context.Context, op Operation) (Operation, error) {
	if op.State != "running" && op.State != "indeterminate" && op.State != "awaiting-local-approval" {
		return op, nil
	}
	reply, err := c.run(ctx, Request{Command: "operation", RequestID: op.ID})
	if err != nil || !validOperation(reply.Operation, op) {
		return op, nil
	}
	op = mergeOperation(op, *reply.Operation)
	op.UpdatedAt = c.cfg.Clock.Now().UTC()
	if err = c.save(ctx, op); err != nil {
		return Operation{}, err
	}
	return op, nil
}
func validOperation(v *Operation, old Operation) bool {
	if v == nil || v.ID != old.ID || v.Action != old.Action || v.PreviewHash != old.PreviewHash {
		return false
	}
	switch v.State {
	case "prepared", "running", "succeeded", "failed", "indeterminate", "awaiting-local-approval":
		return len(v.Result) <= 1000 && len(v.Error) <= 1000
	}
	return false
}
func mergeOperation(old, updated Operation) Operation {
	old.State = updated.State
	old.Result = updated.Result
	old.Error = updated.Error
	old.EffectiveDigest = updated.EffectiveDigest
	return old
}

func (c *Client) Operations(ctx context.Context, before int64, limit int) ([]Operation, error) {
	if before < 0 || limit < 1 || limit > 501 {
		return nil, model.Issues{{Path: "pagination", Message: "before must be nonnegative and limit must be between 1 and 501"}}
	}
	rows, err := c.cfg.DB.Reader().QueryContext(ctx, `SELECT seq,operation_body FROM security_host_operations WHERE (?=0 OR seq<?) ORDER BY seq DESC LIMIT ?`, before, before, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []Operation{}
	for rows.Next() {
		var op Operation
		var raw string
		if err = rows.Scan(&op.Seq, &raw); err != nil {
			return nil, err
		}
		seq := op.Seq
		if err = json.Unmarshal([]byte(raw), &op); err != nil {
			return nil, err
		}
		op.Seq = seq
		out = append(out, op)
	}
	return out, rows.Err()
}
func (c *Client) lookup(ctx context.Context, id string) (Operation, string, Preview, error) {
	var op Operation
	var p Preview
	var digest, raw, praw string
	err := c.cfg.DB.Reader().QueryRowContext(ctx, `SELECT seq,request_digest,operation_body,preview_body FROM security_host_operations WHERE id=?`, id).Scan(&op.Seq, &digest, &raw, &praw)
	if errors.Is(err, sql.ErrNoRows) {
		return op, "", p, ErrNotFound
	}
	if err != nil {
		return op, "", p, err
	}
	seq := op.Seq
	if err = json.Unmarshal([]byte(raw), &op); err != nil {
		return op, "", p, err
	}
	op.Seq = seq
	err = json.Unmarshal([]byte(praw), &p)
	return op, digest, p, err
}
func (c *Client) save(ctx context.Context, op Operation) error {
	raw, err := json.Marshal(op)
	if err != nil {
		return err
	}
	return c.cfg.DB.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE security_host_operations SET operation_body=?,updated_at=? WHERE id=?`, string(raw), op.UpdatedAt.UnixMilli(), op.ID)
		return err
	})
}
