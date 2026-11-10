package deployhelper

import (
	"strings"
	"testing"
)

func TestBackupRequestsDecode(t *testing.T) {
	const opID = `"operationId":"0b7c4a1e-3f2d-4c5b-9a8e-123456789abc"`
	const set = "20261101T120000Z-0a1b2c3d"
	for _, in := range []string{
		`{"version":1,"op":"backup",` + opID + `,"service":"web","timeoutSec":600,"sources":[{"type":"files","path":"media"},{"type":"sqlite","path":"db/app.sqlite"},` +
			`{"type":"postgres","database":"app"},{"type":"mongodb","database":"app","users":true},{"type":"command","argv":["bin/dump","--all"]}],"mode":"stopped","runtime":"node24"}`,
		`{"version":1,"op":"restore",` + opID + `,"service":"web","backupId":"` + set + `"}`,
		`{"version":1,"op":"restore",` + opID + `,"service":"web","backupId":"pre-restore-` + set + `","manifestSha256":"` + strings.Repeat("a", 64) + `"}`,
		`{"version":1,"op":"backups",` + opID + `,"service":"web"}`,
		`{"version":1,"op":"prune",` + opID + `,"service":"web","keep":{"daily":7,"weekly":4}}`,
		`{"version":1,"op":"prune",` + opID + `,"service":"web","keep":{"daily":0,"weekly":1}}`,
	} {
		if _, err := DecodeRequest([]byte(in)); err != nil {
			t.Errorf("%s: %v", in, err)
		}
	}
	backup := func(sources string) string {
		return `{"version":1,"op":"backup",` + opID + `,"service":"web","timeoutSec":60,"sources":` + sources + `}`
	}
	for in, want := range map[string]string{
		backup(`[]`):                                                                                                       "between 1 and 16",
		backup(`[{"type":"files","path":"."}]`):                                                                            "not \".\"",
		backup(`[{"type":"files","path":"../etc"}]`):                                                                       "clean relative path",
		backup(`[{"type":"files","path":"/etc"}]`):                                                                         "clean relative path",
		backup(`[{"type":"files","path":"a//b"}]`):                                                                         "clean relative path",
		backup(`[{"type":"files","path":"a b"}]`):                                                                          "clean relative path",
		backup(`[{"type":"files","path":"x.lcm-new"}]`):                                                                    "clean relative path",
		backup(`[{"type":"files","path":"a"},{"type":"sqlite","path":"a/b.db"}]`):                                          "overlap",
		backup(`[{"type":"files","path":"a"},{"type":"files","path":"a"}]`):                                                "overlap",
		backup(`[{"type":"files","path":"a","database":"x"}]`):                                                             "does not take database",
		backup(`[{"type":"postgres","database":"a b"}]`):                                                                   "database name",
		backup(`[{"type":"postgres","database":"-x"}]`):                                                                    "database name",
		backup(`[{"type":"postgres","database":"postgresql://h/db"}]`):                                                     "database name",
		backup(`[{"type":"postgres","database":"app","users":true}]`):                                                      "postgres does not take users",
		backup(`[{"type":"command","argv":[]}]`):                                                                           "release-relative",
		backup(`[{"type":"command","argv":["/bin/sh","-c"]}]`):                                                             "release-relative",
		backup(`[{"type":"command","argv":["bin/dump","$(id)"]}]`):                                                         "argument",
		backup(`[{"type":"mysql","database":"x"}]`):                                                                        "not a source type",
		backup(`[{"type":"files","path":"a","extra":1}]`):                                                                  "unknown field",
		strings.Replace(backup(`[{"type":"files","path":"a"}]`), `"timeoutSec":60`, `"timeoutSec":0`, 1):                   "timeoutSec must be between",
		strings.Replace(backup(`[{"type":"files","path":"a"}]`), `"timeoutSec":60`, `"timeoutSec":60,"mode":"later"`, 1):   "mode must be",
		strings.Replace(backup(`[{"type":"files","path":"a"}]`), `"timeoutSec":60`, `"timeoutSec":60,"runtime":"Node"`, 1): "not a runtime name",
		strings.Replace(backup(`[{"type":"files","path":"a"}]`), `"timeoutSec":60`, `"timeoutSec":60,"commit":"x"`, 1):     "backup does not take commit",
		`{"version":1,"op":"restore",` + opID + `,"service":"web","backupId":"../x"}`:                                      "not a backup ID",
		`{"version":1,"op":"restore",` + opID + `,"service":"web","backupId":"latest"}`:                                    "not a backup ID",
		`{"version":1,"op":"restore",` + opID + `,"service":"web","backupId":"pre-` + set + `"}`:                           "not a backup ID",
		`{"version":1,"op":"restore",` + opID + `,"service":"web","backupId":"` + set + `","manifestSha256":"A"}`:          "manifestSha256",
		`{"version":1,"op":"restore",` + opID + `,"service":"web","backupId":"` + set + `","mode":"online"}`:               "restore does not take mode",
		`{"version":1,"op":"prune",` + opID + `,"service":"web"}`:                                                          "keep is required",
		`{"version":1,"op":"prune",` + opID + `,"service":"web","keep":{"daily":0,"weekly":0}}`:                            "at least one",
		`{"version":1,"op":"prune",` + opID + `,"service":"web","keep":{"daily":-1,"weekly":1}}`:                           "keep.daily must be",
		`{"version":1,"op":"prune",` + opID + `,"service":"web","keep":{"daily":367,"weekly":1}}`:                          "keep.daily must be",
		`{"version":1,"op":"prune",` + opID + `,"service":"web","keep":{"daily":1,"weekly":1,"yearly":1}}`:                 "unknown field",
		`{"version":1,"op":"backups",` + opID + `,"service":"web","keep":{"daily":1}}`:                                     "backups does not take keep",
	} {
		_, err := DecodeRequest([]byte(in))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", in, err, want)
		}
	}
}

func TestBackupOperationsChange(t *testing.T) {
	for op, changes := range map[Op]bool{OpBackup: true, OpRestore: true, OpPrune: true, OpBackups: false, OpStatus: false} {
		if op.Changes() != changes {
			t.Errorf("%s.Changes() = %v", op, !changes)
		}
	}
}

func TestOversizedListingIsDropped(t *testing.T) {
	resp := Response{Version: Version, Op: OpBackups, OperationID: "0b7c4a1e-3f2d-4c5b-9a8e-123456789abc"}
	for range 300 {
		resp.Backups = append(resp.Backups, BackupInfo{BackupID: "20261101T120000Z-0a1b2c3d", Kind: KindBackup, Problem: strings.Repeat("x", 1000)})
	}
	raw := encodeResponse(resp)
	if len(raw) > MaxResponse {
		t.Fatalf("reply of %d bytes", len(raw))
	}
	got, err := DecodeResponse(raw)
	if err != nil || got.Backups != nil || !strings.Contains(got.Message, "too large") {
		t.Errorf("reply %+v, %v", got, err)
	}
}
