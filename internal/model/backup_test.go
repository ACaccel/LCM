package model_test

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/model"
)

const typedBackup = `    backup:
      sources:
        - { type: files, path: media/ }
        - { type: sqlite, path: app.sqlite }
        - { type: postgres, database: app }
        - { type: mongodb, database: app, users: true }
        - { type: command, argv: [bin/dump, --all] }
      mode: stopped
      timeout: 45m
      keep: { daily: 3, weekly: 2 }
`

func TestTypedBackup(t *testing.T) {
	r, err := model.DecodeResource([]byte(managedService + typedBackup))
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ := r.Service()
	b := cfg.Backup
	if !b.Typed() || !b.Restorable() || b.Sources[0].Path != "media" || b.EffectiveMode() != model.BackupStopped ||
		b.EffectiveTimeout() != 45*time.Minute || b.EffectiveKeep() != (model.BackupKeep{Daily: 3, Weekly: 2}) {
		t.Fatalf("backup = %+v", b)
	}
	enc, err := model.Encode(r)
	if err != nil {
		t.Fatal(err)
	}
	if r2, err := model.DecodeResource(enc); err != nil || !reflect.DeepEqual(r, r2) {
		t.Fatalf("round trip: %v\n%s", err, enc)
	}

	minimal := strings.Replace(managedService+typedBackup, "      mode: stopped\n      timeout: 45m\n      keep: { daily: 3, weekly: 2 }\n", "", 1)
	r, err = model.DecodeResource([]byte(minimal))
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ = r.Service()
	if cfg.Backup.EffectiveMode() != model.BackupOnline || cfg.Backup.EffectiveKeep() != (model.BackupKeep{Daily: 7, Weekly: 4}) ||
		cfg.Backup.EffectiveTimeout() != 30*time.Minute {
		t.Fatalf("defaults = %+v", cfg.Backup)
	}

	bad := []struct{ name, from, to, path, msg string }{
		{"command form on managed", typedBackup, "    backup: { command: tar }\n", "spec.config.backup.sources", "required for adapter managed"},
		{"both forms", "      mode: stopped\n", "      mode: stopped\n      command: tar\n", "spec.config.backup.command", "cannot be combined"},
		{"mode", "mode: stopped", "mode: later", "spec.config.backup.mode", "online or stopped"},
		{"timeout", "timeout: 45m", "timeout: 3h", "spec.config.backup.timeout", "at most 2h0m0s"},
		{"keep", "daily: 3", "daily: 400", "spec.config.backup.keep.daily", "between 0 and 366"},
		{"absolute path", "path: media/", "path: /var/lib/app/media", "spec.config.backup.sources[0].path", "not absolute"},
		{"dotdot path", "path: media/", "path: ../x", "spec.config.backup.sources[0].path", "without"},
		{"whole data directory", "path: media/", "path: .", "spec.config.backup.sources[0].path", "not \".\""},
		{"overlap", "path: app.sqlite", "path: media/app.sqlite", "spec.config.backup.sources", "overlap"},
		{"database name", "database: app }", "database: 'a b' }", "spec.config.backup.sources[2].database", "database name"},
		{"unknown type", "type: postgres", "type: mysql", "spec.config.backup.sources[2].type", "must be one of"},
		{"foreign field", "type: postgres, database: app", "type: postgres, database: app, path: x", "spec.config.backup.sources[2].path", "not valid for source type postgres"},
		{"users on postgres", "type: postgres, database: app", "type: postgres, database: app, users: true", "spec.config.backup.sources[2].users", "not valid for source type postgres"},
		{"command argv", "argv: [bin/dump, --all]", "argv: ['/bin/sh', -c]", "spec.config.backup.sources[4].argv", "release-relative"},
		{"command argument", "--all", "'a b'", "spec.config.backup.sources[4].argv", "may only contain"},
	}
	for _, tt := range bad {
		t.Run(tt.name, func(t *testing.T) {
			doc := managedService + typedBackup
			if !strings.Contains(doc, tt.from) {
				t.Fatalf("fixture lacks %q", tt.from)
			}
			_, err := model.DecodeResource([]byte(strings.Replace(doc, tt.from, tt.to, 1)))
			assertIssue(t, err, tt.path, tt.msg)
		})
	}

	// Sources belong to the managed adapter; the command form to the rest.
	_, err = model.DecodeResource([]byte(resourceHead + "spec:\n  type: service\n  adapter: systemd\n  config:\n    unit: app.service\n" + typedBackup))
	assertIssue(t, err, "spec.config.backup.sources", "only valid for adapter managed")
	_, err = model.DecodeResource([]byte(resourceHead + "spec:\n  type: service\n  adapter: systemd\n  config:\n    unit: app.service\n    backup: { command: x, mode: online }\n"))
	assertIssue(t, err, "spec.config.backup.mode", "needs sources")
}
