package catalog

import (
	"fmt"

	"github.com/ACaccel/lcm/internal/message"
	"github.com/ACaccel/lcm/internal/signal"
)

// Timeline kinds written by config sync; the catalog owns every timeline
// kind so that it can describe each entry.
const (
	KindConfigConflict = "config.conflict"
	KindConfigInvalid  = "config.invalid"
)

// Reasons why a config sync conflict ended, stored as the entry's
// data.reason.
const (
	ConflictResolvedGit     = "resolvedGit"
	ConflictResolvedCurrent = "resolvedCurrent"
	ConflictReleased        = "released"
	ConflictAgreed          = "agreed"
	ConflictLocalMatches    = "localMatches"
)

// EntryMessage returns the summary of a timeline entry with a code for
// translation. The code is derived from the entry's kind and data, so that
// stored entries get one as well; an entry whose data lacks what the code
// needs (written by an older version) keeps its English summary only.
func EntryMessage(e Entry) message.Message {
	d := e.Data
	str := func(m map[string]any, k string) string {
		switch v := m[k].(type) {
		case string:
			return v
		case nil:
			return ""
		default:
			return fmt.Sprint(v)
		}
	}
	has := func(k string) bool { _, ok := d[k]; return ok }
	// Entries copied from signals keep the signal's data one level down.
	inner, _ := d["data"].(map[string]any)
	text := func() message.Message { return message.Text(e.Summary) }
	m := func(code string, kv ...string) message.Message { return message.New(code, e.Summary, kv...) }

	switch e.Kind {
	case KindConfig:
		switch {
		case has("phase"):
			return m("timeline.phase", "previous", str(d, "previous"), "phase", str(d, "phase"), "actor", str(d, "actor"), "reason", str(d, "reason"))
		case str(d, "restoredFrom") != "" && str(d, "restoredFrom") != "0":
			return m("timeline.configRestored", "version", str(d, "version"), "from", str(d, "restoredFrom"), "actor", str(d, "actor"))
		case has("version"):
			return m("timeline.configSaved", "version", str(d, "version"), "actor", str(d, "actor"))
		}
	case KindLifecycle:
		if has("state") {
			return m("timeline.lifecycle", "previous", str(d, "previous"), "state", str(d, "state"), "action", str(d, "action"))
		}
	case KindDeployment, KindAction, KindBackup, KindJob:
		if has("action") && has("outcome") {
			return m("timeline.action", "action", str(d, "action"), "outcome", str(d, "outcome"), "job", str(d, "job"), "version", shortSHA(str(d, "version")),
				"instance", str(d, "instance"))
		}
	case KindRoute:
		if has("state") {
			return m("timeline.route", "state", str(d, "state"), "message", str(d, "message"))
		}
	case string(signal.KindCheck):
		if inner != nil {
			return m("timeline.check", "check", str(inner, "check"), "previous", str(inner, "previous"), "status", str(inner, "status"), "message", str(inner, "message"))
		}
	case string(signal.KindHealth):
		if inner != nil {
			return m("timeline.health", "previous", str(inner, "previous"), "health", str(inner, "health"))
		}
	case string(signal.KindAlert):
		if inner != nil {
			return m("timeline.alert", "alert", str(inner, "alert"), "status", str(inner, "status"))
		}
	case KindConfigConflict:
		switch {
		case d["resolved"] == true && has("reason"):
			return m("timeline.configResolved", "source", str(d, "source"), "reason", str(d, "reason"))
		case d["resolved"] == true:
			return text()
		case d["adopt"] == true:
			return m("timeline.configAdopt", "source", str(d, "source"), "commit", shortSHA(str(d, "commit")), "path", str(d, "path"))
		case d["adopt"] == false:
			return m("timeline.configConflict", "source", str(d, "source"), "commit", shortSHA(str(d, "commit")), "path", str(d, "path"))
		}
	case KindConfigInvalid:
		if has("issues") {
			return m("timeline.configInvalid", "source", str(d, "source"), "commit", shortSHA(str(d, "commit")), "path", str(d, "path"), "issues", str(d, "issues"))
		}
	}
	return text()
}
