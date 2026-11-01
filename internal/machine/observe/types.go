// Package observe provides bounded, read-only local machine observations.
// It owns no scheduler, persistence, health policy, or power operations.
package observe

import (
	"context"
	"time"
)

// FieldVersion versions normalized snapshots and journal events, not journald's
// private cursor format. Readers reject unsupported versions at construction.
const FieldVersion = 1

type CoverageState string

const (
	Available   CoverageState = "available"
	Partial     CoverageState = "partial"
	Unavailable CoverageState = "unavailable"
	Unsupported CoverageState = "unsupported"
	Disabled    CoverageState = "disabled"
)

type Reason string

const (
	ReasonNone          Reason = ""
	ReasonPermission    Reason = "permission_denied"
	ReasonMissing       Reason = "source_missing"
	ReasonInvalid       Reason = "invalid_data"
	ReasonRead          Reason = "read_failed"
	ReasonCommand       Reason = "command_failed"
	ReasonTimeout       Reason = "timeout"
	ReasonCancelled     Reason = "cancelled"
	ReasonTruncated     Reason = "output_truncated"
	ReasonNotConfigured Reason = "not_configured"
	ReasonNotSystemd    Reason = "not_systemd"
	ReasonVersion       Reason = "unsupported_version"
	ReasonCursor        Reason = "cursor_unavailable"
	ReasonRotation      Reason = "journal_changed"
	ReasonLookback      Reason = "lookback_exceeded"
	ReasonLimit         Reason = "batch_limit"
)

type Coverage struct {
	State  CoverageState `json:"state"`
	Reason Reason        `json:"reason,omitempty"`
	// Limitations describe what a successful read cannot prove. They never
	// contain command output, raw identity data, or journal messages.
	Limitations []string `json:"limitations,omitempty"`
}

// Sample's Value is usable only when Coverage.State is Available or Partial.
// A failed sample always carries the zero value, never a previous observation.
type Sample[T any] struct {
	Value    T        `json:"value"`
	Coverage Coverage `json:"coverage"`
}

type Probe interface {
	Observe(context.Context) (Snapshot, error)
}

type Snapshot struct {
	FieldVersion int                   `json:"fieldVersion"`
	ObservedAt   time.Time             `json:"observedAt"`
	Identity     Sample[Identity]      `json:"identity"`
	BootID       Sample[string]        `json:"bootId"`
	OS           Sample[OS]            `json:"os"`
	Init         Sample[Init]          `json:"init"`
	Uptime       Sample[time.Duration] `json:"uptime"`
	CPU          Sample[CPU]           `json:"cpu"`
	Load         Sample[Load]          `json:"load"`
	Memory       Sample[Memory]        `json:"memory"`
	Filesystems  []Filesystem          `json:"filesystems"`
	Interfaces   []Interface           `json:"interfaces"`
	Pressure     []Pressure            `json:"pressure"`
	Units        []Unit                `json:"units"`
}

// Identity is a domain-separated SHA-256 hash of a validated machine-id.
// It is not proof of physical hardware identity: clones can share machine-id.
type Identity struct {
	Hash   string `json:"hash"`
	Scheme string `json:"scheme"`
}

type OS struct {
	ID        string `json:"id"`
	VersionID string `json:"versionId"`
}

type Init struct {
	Name    string `json:"name"`
	Systemd bool   `json:"systemd"`
}

// CPU contains cumulative /proc/stat ticks. Guest counters are included in
// User/Nice already; do not add them again when computing durable deltas.
// A first sample, boot change, or counter reset cannot yield CPU utilization.
type CPU struct {
	User      uint64 `json:"user"`
	Nice      uint64 `json:"nice"`
	System    uint64 `json:"system"`
	Idle      uint64 `json:"idle"`
	IOWait    uint64 `json:"ioWait"`
	IRQ       uint64 `json:"irq"`
	SoftIRQ   uint64 `json:"softIrq"`
	Steal     uint64 `json:"steal"`
	Guest     uint64 `json:"guest"`
	GuestNice uint64 `json:"guestNice"`
	Count     int    `json:"count"`
}

type Load struct {
	One     float64 `json:"one"`
	Five    float64 `json:"five"`
	Fifteen float64 `json:"fifteen"`
}

type Memory struct {
	TotalBytes     uint64 `json:"totalBytes"`
	AvailableBytes uint64 `json:"availableBytes"`
	SwapTotalBytes uint64 `json:"swapTotalBytes"`
	SwapFreeBytes  uint64 `json:"swapFreeBytes"`
}

type Filesystem struct {
	Path string `json:"path"`
	Sample[Capacity]
}

type Capacity struct {
	TotalBytes     uint64 `json:"totalBytes"`
	FreeBytes      uint64 `json:"freeBytes"`
	AvailableBytes uint64 `json:"availableBytes"`
	Inodes         uint64 `json:"inodes"`
	FreeInodes     uint64 `json:"freeInodes"`
}

type Interface struct {
	Name string `json:"name"`
	Sample[Link]
}

type Link struct {
	OperState string `json:"operState"`
}

type Pressure struct {
	Resource string `json:"resource"`
	Sample[PSI]
}

type PSI struct {
	Some *Stall `json:"some,omitempty"`
	Full *Stall `json:"full,omitempty"`
}

type Stall struct {
	Avg10       float64 `json:"avg10"`
	Avg60       float64 `json:"avg60"`
	Avg300      float64 `json:"avg300"`
	TotalMicros uint64  `json:"totalMicros"`
}

type Unit struct {
	Name string `json:"name"`
	Sample[UnitState]
}

type UnitState struct {
	LoadState   string `json:"loadState"`
	ActiveState string `json:"activeState"`
	SubState    string `json:"subState"`
}
