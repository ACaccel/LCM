package router

import (
	"strings"

	"github.com/ACaccel/lcm/internal/message"
	"github.com/ACaccel/lcm/internal/resource"
)

// CoverageView is a source coverage as clients read it: the stored coverage
// with a translatable message for its reason.
type CoverageView struct {
	Coverage
	ReasonMessage *message.Message `json:"reasonMessage,omitempty"`
}

// SnapshotView is a snapshot as clients read it: the stored snapshot with
// translatable messages for the reasons of the attempt and of each source.
type SnapshotView struct {
	Snapshot
	ReasonMessage *message.Message        `json:"reasonMessage,omitempty"`
	Sources       map[string]CoverageView `json:"sources"`
}

// NewSnapshotView adds reason messages to a stored snapshot.
func NewSnapshotView(s Snapshot) SnapshotView {
	v := SnapshotView{Snapshot: s, Sources: make(map[string]CoverageView, len(s.Sources))}
	if s.Reason != "" {
		m := resource.ReasonMessage(s.Reason)
		v.ReasonMessage = &m
	}
	for name, c := range s.Sources {
		cv := CoverageView{Coverage: c}
		if c.Reason != "" {
			m := coverageMessage(c.Reason)
			cv.ReasonMessage = &m
		}
		v.Sources[name] = cv
	}
	return v
}

// coverageMessage returns the message of a source's reason, which the
// Xiaomi client produces from the router's answer: a fixed phrase, or one
// with the HTTP status or the router's error code.
func coverageMessage(reason string) message.Message {
	switch reason {
	case "transport error":
		return message.New("router.transportError", reason)
	case "invalid json":
		return message.New("router.invalidJson", reason)
	case "missing code":
		return message.New("router.missingCode", reason)
	}
	if v, ok := strings.CutPrefix(reason, "http "); ok && v != "" {
		return message.New("router.http", reason, "status", v)
	}
	if v, ok := strings.CutPrefix(reason, "code "); ok && v != "" {
		return message.New("router.code", reason, "code", v)
	}
	return message.Text(reason)
}
