// Package securityhost brokers fixed, root-owned workstation security controls.
// It cannot execute arbitrary commands or accept file paths from an API caller.
package securityhost

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/ACaccel/lcm/internal/audit"
	"github.com/ACaccel/lcm/internal/clock"
	"github.com/ACaccel/lcm/internal/store"
)

var (
	ErrConflict    = errors.New("securityhost: request or effective configuration conflict")
	ErrUnavailable = errors.New("securityhost: restricted host helper unavailable")
	ErrNotFound    = errors.New("securityhost: operation not found")
)

const ProtocolVersion = 1

// Config's default runner invokes one immutable helper through sudo -n.
// A custom runner is a dependency injection boundary, never an API setting.
type Config struct {
	DB        *store.DB
	Audit     *audit.Log
	Clock     clock.Clock
	MachineID string
	Runner    Runner
}

type Runner interface {
	Run(context.Context, Request) (Response, error)
}

type Request struct {
	Version        int             `json:"version"`
	Command        string          `json:"command"`
	MachineID      string          `json:"machineId"`
	RequestID      string          `json:"requestId,omitempty"`
	Action         string          `json:"action,omitempty"`
	Reason         string          `json:"reason,omitempty"`
	Actor          string          `json:"actor,omitempty"`
	ExpectedDigest string          `json:"expectedDigest,omitempty"`
	Settings       json.RawMessage `json:"settings,omitempty"`
	PreviewHash    string          `json:"previewHash,omitempty"`
}

type Response struct {
	Version   int         `json:"version"`
	Status    *HostStatus `json:"status,omitempty"`
	Preview   *Preview    `json:"preview,omitempty"`
	Operation *Operation  `json:"operation,omitempty"`
	Error     string      `json:"error,omitempty"`
}

type Capability struct {
	Action  string `json:"action"`
	Enabled bool   `json:"enabled"`
	Reason  string `json:"reason"`
}
type Profile struct {
	Name     string `json:"name"`
	Enabled  bool   `json:"enabled"`
	Required bool   `json:"required"`
}
type Schedule struct {
	Hour   int `json:"hour"`
	Minute int `json:"minute"`
}
type Limit struct {
	Category    string `json:"category"`
	Rate        string `json:"rate"`
	Burst       int    `json:"burst"`
	Connections int    `json:"connections"`
}
type AIDEStatus struct {
	Targets           []string   `json:"targets"`
	Result            *string    `json:"result"`
	LastRunAt         *time.Time `json:"lastRunAt"`
	NextRunAt         *time.Time `json:"nextRunAt"`
	BaselineCreatedAt *time.Time `json:"baselineCreatedAt"`
	BaselineDigest    *string    `json:"baselineDigest"`
	Schedule          *Schedule  `json:"schedule"`
	Profiles          []Profile  `json:"profiles"`
	ReportSummary     *string    `json:"reportSummary"`
}
type AuditStatus struct {
	Enabled     *int      `json:"enabled"`
	Lost        *int64    `json:"lost"`
	Backlog     *int64    `json:"backlog"`
	RulesLoaded *bool     `json:"rulesLoaded"`
	Profiles    []Profile `json:"profiles"`
}
type NginxStatus struct {
	Mode        *string `json:"mode"`
	DryRunOnly  bool    `json:"dryRunOnly"`
	R11Approved bool    `json:"r11Approved"`
	Limits      []Limit `json:"limits"`
}
type CollectorStatus struct {
	ObservedAt    *time.Time     `json:"observedAt"`
	LastDelivery  *time.Time     `json:"lastDelivery"`
	Queued        *int64         `json:"queued"`
	Dropped       *int64         `json:"dropped"`
	DeliveryError *string        `json:"deliveryError"`
	Health        map[string]any `json:"health"`
}
type HostStatus struct {
	Available       bool            `json:"available"`
	Reason          string          `json:"reason"`
	MachineID       string          `json:"machineId"`
	EffectiveDigest string          `json:"effectiveDigest"`
	Capabilities    []Capability    `json:"capabilities"`
	AIDE            AIDEStatus      `json:"aide"`
	Audit           AuditStatus     `json:"audit"`
	Nginx           NginxStatus     `json:"nginx"`
	Collector       CollectorStatus `json:"collector"`
}

type PreviewRequest struct {
	RequestID      string          `json:"requestId"`
	Action         string          `json:"action"`
	Reason         string          `json:"reason"`
	ExpectedDigest string          `json:"expectedDigest"`
	Settings       json.RawMessage `json:"settings"`
}
type Preview struct {
	RequestID             string    `json:"requestId"`
	Action                string    `json:"action"`
	PreviewHash           string    `json:"previewHash"`
	ExpiresAt             time.Time `json:"expiresAt"`
	Changes               []string  `json:"changes"`
	RequiresLocalApproval bool      `json:"requiresLocalApproval"`
}
type ApplyRequest struct {
	RequestID   string `json:"requestId"`
	PreviewHash string `json:"previewHash"`
	Reason      string `json:"reason"`
}
type Operation struct {
	Seq             int64     `json:"seq"`
	ID              string    `json:"id"`
	Action          string    `json:"action"`
	State           string    `json:"state"`
	Reason          string    `json:"reason"`
	Actor           string    `json:"actor"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
	Result          string    `json:"result"`
	Error           string    `json:"error"`
	PreviewHash     string    `json:"previewHash"`
	EffectiveDigest string    `json:"effectiveDigest"`
}
