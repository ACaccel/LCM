// Package deployhelper is the deploy helper, a restricted root program that
// builds, installs and runs the services allowlisted in /etc/lcm/host.json,
// and the client LCM calls it with. This comment is the protocol reference.
//
// # Invocation and trust
//
// LCM runs "/usr/bin/sudo -n -- /usr/local/libexec/lcm-deploy". The sudoers
// rule /etc/sudoers.d/lcm-deploy allows exactly that, without arguments;
// the root-owned wrapper clears the environment and executes the pinned
// helper "/usr/local/libexec/lcm-deploy-helper deploy-helper", a copy of the
// reviewed build that last ran "lcm host apply", never the self-deployed
// binary. The helper reads one request from stdin and writes one reply to
// stdout. It takes nothing from its environment, arguments or the request
// beyond the fields below: what a service may use (its port, runtimes and
// build inputs) comes only from the services section of host.json, which
// the helper reads itself with the trust checks of "lcm host apply"
// (root-owned, not writable by group or others, no links). A request that
// names a service, runtime or input that host.json does not grant is
// refused. Documents carry no privileges and cannot widen the allowlist.
//
// # Messages
//
// A request is one JSON object of at most MaxRequest bytes, a reply one
// JSON object of at most MaxResponse bytes. Decoding is strict: unknown or
// duplicate keys, trailing data, invalid UTF-8 and fields that the
// operation does not take are refused. Every request carries "version" (1),
// "op" and "operationId", a lower-case UUID that LCM chooses and never
// reuses.
//
//	op        fields besides version, op, operationId
//	build     service, commit, command, timeoutSec, runtimes (optional)
//	install   service, buildId, artifact
//	activate  service, releaseId, start (optional, see below), runtime (optional),
//	          migrate (optional), keepReleases (optional, 2-20, default 3),
//	          publish (optional), limits (optional)
//	start     service
//	stop      service
//	restart   service
//	status    service
//	releases  service
//	query     (operationId names the operation to look up)
//	backup    service, sources, mode (optional), timeoutSec, runtime (optional)
//	restore   service, backupId, manifestSha256 (optional), restoreUsers (optional)
//	backups   service
//	prune     service, keep
//	ingress.apply   service, route (optional)
//	ingress.status  (no fields besides version, op, operationId)
//
// Field formats: service as in host.json; commit is a full lower-case
// SHA-1 or SHA-256 commit ID; command is a shell script (at most 16 KiB);
// timeoutSec is 1-7200; buildId and releaseId are IDs the helper returned
// (UTC "20060102T150405Z-" and 8 hex digits); artifact is a clean relative
// directory path inside the build tree ("." is the whole tree); start is a
// release-relative file followed by at most 32 arguments; migrate is a
// program followed by at most 32 arguments, the program being a
// release-relative executable or the name of a program in the runtime's
// bin directory. Paths use letters, digits and "._-/" without ".."
// components; arguments also allow "=:,@+". publish is {dir, retain,
// retainDays}: dir a release-relative directory ("." is the whole release),
// retain at most 16 directories below it that end with "/" and have no
// component starting with ".", retainDays 1-90 (default 14, only with
// retain). limits is {memoryHigh, memoryMax, stopTimeoutSec}: sizes of
// digits with K, M or G between 1M and 16G, memoryHigh not above
// memoryMax, stopTimeoutSec 1-600. An activate without start publishes
// only: it needs publish and takes neither migrate nor limits. The fields
// of backup, restore and prune are described under "Backups".
//
// A reply carries "version", "op" and "operationId" of the request, and:
//
//   - "state" for the operations that change something (build, install,
//     activate, start, stop, restart, backup, restore, prune, ingress.apply)
//     and for query: "succeeded", "failed",
//     "running" (query only: the operation is still in progress) or
//     "interrupted" (query only: the helper stopped before it finished; what
//     it did is unknown and must be inspected on the host).
//   - "code" and "message" when the request was not carried out or failed:
//     "refused" (invalid request, or not allowed by host.json or the host's
//     state), "not_found" (unknown build, release, unit or operation),
//     "conflict" (the operationId was used for another request), "busy"
//     (another operation of the service is running; nothing was done),
//     "disabled" (host.json does not enable the component of the operation,
//     components.ingress for ingress.*) or "failed" (the operation started
//     and failed). message is English text
//     with control characters replaced.
//   - "log": the sanitized tail (at most MaxLog bytes) of the output of the
//     build, migration or systemctl command.
//   - one result object: "build" {buildId, commit}, "install" {releaseId,
//     manifest}, "activate" {releaseId, previous, switched, unitChanged,
//     published {written, replaced, unchanged, deleted, retained, skipped}},
//     "unit" (status) {loadState, activeState, subState, unitFileState,
//     mainPid, release}, "releases" [{releaseId, current, previous,
//     manifest}], "backup" {backupId, manifestSha256, size, files, skipped,
//     createdAt, releaseId}, "restore" {backupId, preRestoreId, restored,
//     skipped, started, leftStopped}, "backups" [{backupId, kind, createdAt,
//     size, manifestSha256, mode, releaseId, sources, skipped, restoredFrom,
//     problem}] (with "backupsTruncated" when the list is cut) or "prune"
//     {removed, kept}, "ingressApply" {sha256, changes, dropped} or "ingress"
//     (ingress.status) {rendered, renderError, installed, enabled, tls,
//     adopted, reverted, last {at, outcome, message, sha256}, dropped,
//     routes [{service, prefix, options}]}.
//
// A release manifest records the service, releaseId, buildId, commit,
// build runtimes, artifact, the SHA-256 of the installed tree (over each
// entry's type, path, mode, size and content or link target, in path
// order), its size in bytes and number of files, and the creation time in
// Unix milliseconds.
//
// # Ledger, retries and locks
//
// The root-only directory /var/lib/lcm-deploy (0700, created by "lcm host
// apply"; the helper never creates it) holds ops/<operationId>.json, one
// record per changing operation: the request's SHA-256, the state and the
// final reply. Each record is written to a new file opened with O_EXCL,
// synced, renamed into place and its directory synced. The record is
// written as "running" before the operation changes anything and replaced
// by the final reply after it; the reply is sent only after that. A
// request whose operationId is already recorded is not carried out again:
// the same request gets the recorded reply, a different one "conflict". So
// after a lost reply or a timeout LCM sends query (or the same request) and
// learns the outcome instead of guessing. Records of finished operations
// are removed 90 days after they finished, and the oldest finished ones
// beyond 10,000 records; the ledger is listed for that at most once an
// hour. status, releases, backups, query and ingress.status are not
// recorded.
//
// locks/<service>.build is locked (flock) for the whole of a build of that
// service, and locks/<service> for the whole of its other changing
// operations; a second operation that needs a held lock gets "busy" at
// once, so a long build never blocks stop or restart. A "running" record
// whose lock is free belongs to a helper that stopped, so query marks it
// "interrupted". Lock files exist only for services that host.json grants.
//
// # Effects
//
// build copies the lcm-owned checkout /var/lib/lcm/work/<service> without
// its .git directory (the build gets the commit as LCM_COMMIT and must not
// rely on git metadata); HEAD (read from .git without running git as root)
// must be the commit. The copy goes to
// /var/lib/lcm-build/<service>/<buildId>/src, owned by the service's
// own build account lcmb-<service>, and runs the command there with
// systemd-run as that account in a sandbox (see buildArgs). It keeps the
// last 3 builds of a service. A build does not start with less than 2 GiB
// or 100,000 inodes free, empties a cache above 4 GiB first, and is stopped
// when its source, home and cache together exceed 16 GiB or 2,000,000
// entries, or when less than 512 MiB or 20,000 inodes remain free (see
// Limits; measured every 10 seconds). install first removes the oldest releases
// beyond 20 that are neither current nor previous, then copies the
// artifact directory of a successful build to
// /opt/<service>/releases/<releaseId>, root-owned, directories 0755 and
// files 0644 or 0755, refusing links that leave it, and writes the
// manifest next to it as <releaseId>.json. activate runs the optional
// migration as the service account in the new release, publishes the
// release's static content (see Publishing), writes
// /etc/systemd/system/<service>.service from the built-in template (with
// limits as MemoryHigh=, MemoryMax= and TimeoutStopSec=), switches the
// /opt/<service>/current and previous links, reloads systemd when the unit
// changed, enables and restarts the unit, and prunes releases beyond
// keepReleases, never the current or previous one. A service whose grant
// has no port runs no process: its activate has no start, publishes and
// switches the links, and writes no unit; a grant with a port needs start.
// start, stop, restart and status act on <service>.service only, and only
// on a unit the helper wrote.
//
// # Publishing
//
// publish copies the directory dir of the release to the service's publish
// directory /opt/<service>/www (root 0755, made by "lcm host allow" and
// "lcm host apply"), which needs the grant's route.static to name it. The
// source must hold only root-owned directories and regular files: a
// symbolic link anywhere in it, or in dir, refuses the activate; entries
// whose name starts with "." are skipped; the bounds of install apply.
// Files that are new or changed are written first through a temporary file
// and a rename, every one but the .html files, and then the .html files one
// by one, so that a page never names an asset that is not there yet. Every
// published file gets the time of the activate as its modification time.
// Then what the release does not contain is removed, except files below a
// retain directory, which stay until their modification time is retainDays
// old: an asset stays that long after the last release that had it. A
// failure before the removal answers "failed" and switches nothing; the new
// files it wrote stay unreferenced and the .html files not yet replaced are
// the old ones. A failure to remove is noted in the log only. A rollback
// activates the earlier release, so it publishes that release's content
// again.
//
// # Backups
//
// backup, restore, backups and prune need "backup.dir" in host.json (an
// absolute directory of its own, root-owned 0700; see lcm host apply) and
// take the service's lock. They work on the service's data directory
// /var/lib/<service> and its current release, and never on anything else.
//
// Fields. backup: sources (1-16), mode ("online", the default, or "stopped"),
// timeoutSec (1-7200) and runtime (optional; a runtime granted to the
// service, whose bin directory is put on the worker's PATH). A source is
// {type, ...} with files {path}, sqlite {path}, postgres {database},
// mongodb {database, users} or command {argv}. A path is a clean relative
// path below the data directory (not ".") and the paths of files and sqlite
// sources may not overlap; a database is a name of letters, digits, "_" and
// "-"; argv is a release-relative executable and arguments, as for start.
// restore: backupId, manifestSha256 (refused when the set's manifest has
// another checksum, so that LCM restores the set it recorded) and
// restoreUsers. restoreUsers (default false) also restores the "admin" users
// and roles of a mongodb source saved with users: true. Those are the whole
// mongod instance's accounts: a plain restore replaces only the database and
// does not copy the accounts either, and users: true is meant for a service
// with a mongod of its own. prune: keep {daily, weekly}, the newest set of
// each of that many UTC days and ISO weeks stays (never the newest set, a
// set whose manifest cannot be read, or a pre-restore set younger than 7
// days).
//
// Layout. <backup.dir>/<service>/<id>/ (root 0700; id as for releases, with
// "pre-restore-" before it for the copy a restore takes) holds manifest.json
// and one artifact per source: NNN-files.tar, NNN-sqlite.db, NNN-postgres.dump,
// NNN-mongodb.archive (with NNN-mongodb-users.archive and
// NNN-mongodb-roles.archive), NNN-command.out, all root 0600. The manifest
// records the service, id, kind, mode, creation time, release, the sources
// with their artifacts (or "absent", for a pre-restore copy of a path that
// did not exist) and each artifact's size and SHA-256. A set is built in
// .tmp-* and renamed into place; .stage-*, .tmp-* and .del-* are leftovers
// that every changing backup operation removes first.
//
// Worker. The artifacts are made by "lcm backup-worker", started with
// systemd-run as the service's own account in a sandbox like its unit's: the
// file system is read-only except the staging output and the data directory
// (which the account owns; a read-only SQLite connection to a cleanly closed
// WAL database must create its -shm file there), the backup directory is
// replaced by an empty read-only one with only this operation's staging
// directories bound back, no ports, 2 GiB of memory, 512 tasks, the
// timeoutSec, and files of at most BackupBytes (32 GiB). The specification is
// written by root; the database tools (pg_dump, pg_restore, mongodump,
// mongorestore) are named in it by absolute path, found by the helper in
// /usr/bin or /usr/local/bin and checked to be root's, with every link and
// directory only root can change, and the worker runs nothing else it finds.
// A MongoDB connection string from the service's environment reaches the
// tools in a 0600 --config file in the worker's private /tmp, not in their
// arguments. The worker marks itself undumpable; programs it starts are
// dumpable again, so this only narrows what the account can reach.
//
// Everything in the staging output is untrusted input. The worker's output is
// measured while it runs. When it ends, root changes the output directory's
// owner to itself (mode 0700) through a descriptor checked against the
// directory it looked at, because the account may still hold a descriptor of
// it, and does the same for every directory below it (bounded in depth,
// entries and time), measures it again, and refuses any entry but the result
// and the expected regular artifacts. A staging tree that still cannot be
// removed is renamed .del-stuck-* and a leftover the sweep cannot remove is
// skipped and named in the reply's log, so neither blocks later operations.
// A file the account already had open for writing can still grow until root
// has copied it (the copy takes the size it saw), so backup.dir should be on
// the same file system as /var/lib or have a quota. Root then copies the artifacts into the new
// set while hashing them. The backup file system must have the reserve
// of StopFreeBytes and StopFreeInodes for those copies beyond the set's size.
//
// mode "stopped" stops the unit first and starts it again afterwards if it
// was running. Every stopping operation always issues the stop for a unit the
// helper wrote, whatever its state: a unit that is activating or waiting to
// restart is not "active" yet would start the service again. It then
// requires the unit to be inactive or failed and otherwise refuses with
// nothing changed.
//
// Restore. The order protects the data. The set's manifest and artifacts are
// verified against their checksums while they are copied to staging; the
// tools are resolved; the free space is checked for the staged set, the
// pre-restore copy (sized from the data being replaced) and the staging of
// an undo; the unit is stopped; a pre-restore set of exactly the restored
// sources is made (reported as preRestoreId); room for an undo is checked
// again; and only then is anything replaced. Files are extracted beside
// their target and swapped in, SQLite databases are swapped in without their
// journal files, and databases are restored with pg_restore --clean and
// mongorestore --drop. Archive entries that are links, devices, special mode
// bits, absolute, with "..", or outside the source are refused before the
// target is touched. A failed replacement is undone by restoring the
// pre-restore set and the unit is started again if it was running. If the
// undo fails too, the unit is left stopped (leftStopped) and the message
// names the set to restore by hand: the data is neither the old nor the new.
// A command source is kept but never restored.
//
// backups lists at most 200 sets, newest first; a set whose manifest cannot
// be used has "problem" instead of details. A listing that does not fit in
// MaxResponse is cut by omitting the sources of the entries, then the
// oldest entries, and the reply sets "backupsTruncated", as it does when
// more than 200 sets exist.
//
// # Ingress
//
// ingress.apply and ingress.status need "components.ingress" in host.json;
// the deploy operations above need "components.deploy", and every one of
// them is refused for a service granted with "external": true, which LCM
// only publishes. The ingress operations share the nginx site of the
// domain (internal/hostsetup, ingress.go): ingress.apply takes the route
// presentation options of one service (internal/ingress.Options; absent
// means the service has none), refuses them unless the service's grant has
// a route and they pass ingress.Options.Validate, renders the whole site
// with them and with the accepted options of every other service (another
// service's options that no longer fit its grant are left out and named in
// "dropped"; the requested ones must fit), and applies it: under
// /run/lcm-ingress.lock (and the configured legacy lock while it exists,
// waiting at most 30 seconds for each), with "nginx -t" of the complete
// configuration, a reload, a probe of the location nginx answers itself on
// every listen address, and a full revert on any failure. The accepted
// options are kept, root-only, in /var/lib/lcm-ingress/routes/<service>.json,
// written before the site is applied and put back when it fails. It
// refuses while the site was never installed, was reverted or is not
// enabled ("lcm host apply" installs it and adopts the hand-made site, so
// that an administrator reviews the cutover), and while the adopted site is
// enabled again; it checks this before and again after taking the locks. Its ledger lock is "lcm-ingress", one
// for all services. ingress.status reports the digest of the site the
// configuration renders, the installed one and the last result; it is not
// recorded.
package deployhelper

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/ACaccel/lcm/internal/hostsetup"
	"github.com/ACaccel/lcm/internal/ingress"
)

// Protocol limits.
const (
	Version     = 1
	MaxRequest  = 64 << 10
	MaxResponse = 256 << 10
	// MaxLog bounds the log tail of a reply.
	MaxLog = 32 << 10
	// MaxCommand bounds a build command.
	MaxCommand = 16 << 10
	// MaxTimeoutSec bounds a build.
	MaxTimeoutSec = 7200
	// DefaultKeepReleases is the number of releases activate keeps when the
	// request does not say; MaxKeepReleases is the most it may keep.
	DefaultKeepReleases = 3
	MaxKeepReleases     = 20
	maxArgs             = 32
)

// Op is a helper operation.
type Op string

// Operations.
const (
	OpBuild    Op = "build"
	OpInstall  Op = "install"
	OpActivate Op = "activate"
	OpStart    Op = "start"
	OpStop     Op = "stop"
	OpRestart  Op = "restart"
	OpStatus   Op = "status"
	OpReleases Op = "releases"
	OpQuery    Op = "query"
	// Typed backups.
	OpBackup  Op = "backup"
	OpRestore Op = "restore"
	OpBackups Op = "backups"
	OpPrune   Op = "prune"
	// Ingress.
	OpIngressApply  Op = "ingress.apply"
	OpIngressStatus Op = "ingress.status"
)

// Ingress reports whether op belongs to the ingress component.
func (op Op) Ingress() bool { return op == OpIngressApply || op == OpIngressStatus }

// Changes reports whether op changes the host and is recorded in the
// ledger.
func (op Op) Changes() bool {
	switch op {
	case OpBuild, OpInstall, OpActivate, OpStart, OpStop, OpRestart, OpBackup, OpRestore, OpPrune, OpIngressApply:
		return true
	}
	return false
}

// Code is the opaque error code of a reply.
type Code string

// Error codes.
const (
	CodeRefused  Code = "refused"
	CodeNotFound Code = "not_found"
	CodeConflict Code = "conflict"
	CodeFailed   Code = "failed"
	CodeBusy     Code = "busy"
	// CodeDisabled: the host configuration does not enable the component
	// the operation belongs to (components.ingress for ingress.*).
	CodeDisabled Code = "disabled"
)

// State is the state of a recorded operation.
type State string

// Operation states.
const (
	StateRunning     State = "running"
	StateSucceeded   State = "succeeded"
	StateFailed      State = "failed"
	StateInterrupted State = "interrupted"
)

// Request is one helper request. Each operation takes only its own fields.
type Request struct {
	Version     int    `json:"version"`
	Op          Op     `json:"op"`
	OperationID string `json:"operationId"`
	Service     string `json:"service,omitempty"`
	// build
	Commit     string   `json:"commit,omitempty"`
	Command    string   `json:"command,omitempty"`
	TimeoutSec int      `json:"timeoutSec,omitempty"`
	Runtimes   []string `json:"runtimes,omitempty"`
	// install
	BuildID  string `json:"buildId,omitempty"`
	Artifact string `json:"artifact,omitempty"`
	// activate
	ReleaseID    string      `json:"releaseId,omitempty"`
	Start        []string    `json:"start,omitempty"`
	Runtime      string      `json:"runtime,omitempty"`
	Migrate      []string    `json:"migrate,omitempty"`
	KeepReleases int         `json:"keepReleases,omitempty"`
	Publish      *Publish    `json:"publish,omitempty"`
	Limits       *UnitLimits `json:"limits,omitempty"`
	// backup (with timeoutSec and runtime)
	Sources []BackupSource `json:"sources,omitempty"`
	Mode    string         `json:"mode,omitempty"`
	// restore
	BackupID       string `json:"backupId,omitempty"`
	ManifestSHA256 string `json:"manifestSha256,omitempty"`
	// RestoreUsers also restores the users and roles of a MongoDB source
	// saved with users: true. They live in the admin database of the whole
	// mongod instance, so restoring them replaces every account of it.
	RestoreUsers bool `json:"restoreUsers,omitempty"`
	// prune
	Keep *Keep `json:"keep,omitempty"`
	// ingress.apply
	Route *ingress.Options `json:"route,omitempty"`
}

// Response is one helper reply.
type Response struct {
	Version     int    `json:"version"`
	Op          Op     `json:"op"`
	OperationID string `json:"operationId,omitempty"`
	State       State  `json:"state,omitempty"`
	Code        Code   `json:"code,omitempty"`
	Message     string `json:"message,omitempty"`
	Log         string `json:"log,omitempty"`

	Build    *BuildResult    `json:"build,omitempty"`
	Install  *InstallResult  `json:"install,omitempty"`
	Activate *ActivateResult `json:"activate,omitempty"`
	Unit     *UnitStatus     `json:"unit,omitempty"`
	Releases []Release       `json:"releases,omitempty"`
	Backup   *BackupResult   `json:"backup,omitempty"`
	Restore  *RestoreResult  `json:"restore,omitempty"`
	Backups  []BackupInfo    `json:"backups,omitempty"`
	// BackupsTruncated reports that backups is incomplete: entries beyond the
	// newest MaxListed were left out, or entries were cut (their sources
	// omitted, then the oldest dropped) to keep the reply within MaxResponse.
	BackupsTruncated bool                     `json:"backupsTruncated,omitempty"`
	Prune            *PruneResult             `json:"prune,omitempty"`
	IngressApply     *IngressApplyResult      `json:"ingressApply,omitempty"`
	Ingress          *hostsetup.IngressStatus `json:"ingress,omitempty"`
}

// IngressApplyResult is the result of ingress.apply: the digest of the
// site and what changed (none when it was up to date).
type IngressApplyResult struct {
	SHA256  string   `json:"sha256"`
	Changes []string `json:"changes"`
	// Dropped describes other services' stored options that no longer fit
	// and were left out (their routes have their defaults).
	Dropped []string `json:"dropped,omitempty"`
}

// BuildResult is the result of build.
type BuildResult struct {
	BuildID string `json:"buildId"`
	Commit  string `json:"commit"`
}

// InstallResult is the result of install.
type InstallResult struct {
	ReleaseID string   `json:"releaseId"`
	Manifest  Manifest `json:"manifest"`
}

// Manifest describes an installed release.
type Manifest struct {
	Version    int      `json:"version"`
	Service    string   `json:"service"`
	ReleaseID  string   `json:"releaseId"`
	BuildID    string   `json:"buildId"`
	Commit     string   `json:"commit"`
	Runtimes   []string `json:"runtimes"`
	Artifact   string   `json:"artifact"`
	TreeSHA256 string   `json:"treeSha256"`
	Size       int64    `json:"size"`
	Files      int      `json:"files"`
	CreatedAt  int64    `json:"createdAt"`
}

// ActivateResult is the result of activate. Switched reports that current
// now names the release, also when a later step failed.
type ActivateResult struct {
	ReleaseID   string `json:"releaseId"`
	Previous    string `json:"previous,omitempty"`
	Switched    bool   `json:"switched"`
	UnitChanged bool   `json:"unitChanged"`
	// Published counts what publishing changed, when the request had
	// publish.
	Published *PublishResult `json:"published,omitempty"`
}

// Publish is the static content of a release to publish.
type Publish struct {
	Dir        string   `json:"dir"`
	Retain     []string `json:"retain,omitempty"`
	RetainDays int      `json:"retainDays,omitempty"`
}

// UnitLimits tighten the unit of a service.
type UnitLimits struct {
	MemoryHigh     string `json:"memoryHigh,omitempty"`
	MemoryMax      string `json:"memoryMax,omitempty"`
	StopTimeoutSec int    `json:"stopTimeoutSec,omitempty"`
}

// PublishResult counts the files of a publish: written (new), replaced
// (changed), unchanged, deleted (not in the release), retained (not in the
// release but below a retain directory and younger than retainDays) and
// skipped (entries of the source starting with ".", with what is below
// them).
type PublishResult struct {
	Written   int `json:"written"`
	Replaced  int `json:"replaced"`
	Unchanged int `json:"unchanged"`
	Deleted   int `json:"deleted"`
	Retained  int `json:"retained"`
	Skipped   int `json:"skipped"`
}

// UnitStatus is the result of status.
type UnitStatus struct {
	LoadState     string `json:"loadState"`
	ActiveState   string `json:"activeState"`
	SubState      string `json:"subState"`
	UnitFileState string `json:"unitFileState"`
	MainPID       int    `json:"mainPid"`
	// Release is the current release, if any.
	Release string `json:"release,omitempty"`
}

// Release is one entry of releases.
type Release struct {
	ReleaseID string    `json:"releaseId"`
	Current   bool      `json:"current"`
	Previous  bool      `json:"previous"`
	Manifest  *Manifest `json:"manifest,omitempty"`
}

// Backup source types.
const (
	SourceFiles    = "files"
	SourceSQLite   = "sqlite"
	SourcePostgres = "postgres"
	SourceMongoDB  = "mongodb"
	SourceCommand  = "command"
)

// Backup modes.
const (
	ModeOnline  = "online"
	ModeStopped = "stopped"
)

// Backup kinds.
const (
	KindBackup     = "backup"
	KindPreRestore = "pre-restore"
)

// MaxSources bounds the sources of one backup.
const MaxSources = 16

// BackupSource is one thing to back up. Each type takes only its own
// fields.
type BackupSource struct {
	Type     string   `json:"type"`
	Path     string   `json:"path,omitempty"`
	Database string   `json:"database,omitempty"`
	Users    bool     `json:"users,omitempty"`
	Argv     []string `json:"argv,omitempty"`
}

// Keep is the retention of backup sets: the number of days and ISO weeks
// that keep their newest set.
type Keep struct {
	Daily  int `json:"daily"`
	Weekly int `json:"weekly"`
}

// BackupResult is the result of backup.
type BackupResult struct {
	BackupID       string `json:"backupId"`
	ManifestSHA256 string `json:"manifestSha256"`
	Size           int64  `json:"size"`
	Files          int    `json:"files"`
	// Skipped counts the entries of file sources that are not regular
	// files or directories and were left out.
	Skipped   int    `json:"skipped"`
	CreatedAt int64  `json:"createdAt"`
	ReleaseID string `json:"releaseId,omitempty"`
}

// RestoreResult is the result of restore. Restored and Skipped name the
// sources (type with path or database) that were restored and that cannot
// be (command artifacts). Started reports that the unit was active before
// and was started again. LeftStopped reports that the restore failed and
// putting the previous data back failed too, so the unit was deliberately
// not started on half-restored data.
type RestoreResult struct {
	BackupID     string   `json:"backupId"`
	PreRestoreID string   `json:"preRestoreId,omitempty"`
	Restored     []string `json:"restored"`
	Skipped      []string `json:"skipped"`
	Started      bool     `json:"started"`
	LeftStopped  bool     `json:"leftStopped,omitempty"`
}

// BackupInfo is one entry of backups. Problem is set instead of the other
// details when the set's manifest cannot be used.
type BackupInfo struct {
	BackupID       string         `json:"backupId"`
	Kind           string         `json:"kind"`
	CreatedAt      int64          `json:"createdAt,omitempty"`
	Size           int64          `json:"size,omitempty"`
	ManifestSHA256 string         `json:"manifestSha256,omitempty"`
	Mode           string         `json:"mode,omitempty"`
	ReleaseID      string         `json:"releaseId,omitempty"`
	Sources        []BackupSource `json:"sources,omitempty"`
	Skipped        int            `json:"skipped,omitempty"`
	RestoredFrom   string         `json:"restoredFrom,omitempty"`
	Problem        string         `json:"problem,omitempty"`
}

// PruneResult is the result of prune: the removed set IDs and the number
// of sets left.
type PruneResult struct {
	Removed []string `json:"removed"`
	Kept    int      `json:"kept"`
}

var (
	uuidPattern   = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	commitPattern = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
	idPattern     = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}Z-[0-9a-f]{8}$`)
	relPattern    = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,256}$`)
	argPattern    = regexp.MustCompile(`^[A-Za-z0-9._/=:,@+-]{1,256}$`)
	binPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$`)
	backupPattern = regexp.MustCompile(`^(?:pre-restore-)?[0-9]{8}T[0-9]{6}Z-[0-9a-f]{8}$`)
	dbPattern     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]{0,62}$`)
	sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// relPath accepts a clean relative path without ".." components.
func relPath(p string) bool {
	if !relPattern.MatchString(p) || path.IsAbs(p) || path.Clean(p) != p {
		return false
	}
	return !slices.Contains(strings.Split(p, "/"), "..")
}

func validArgs(args []string) error {
	if len(args) > maxArgs {
		return fmt.Errorf("at most %d arguments", maxArgs)
	}
	for _, a := range args {
		if !argPattern.MatchString(a) {
			return fmt.Errorf("argument %q may only contain letters, digits and ._/=:,@+-", hostsetup.Printable(a, 64))
		}
	}
	return nil
}

// fields lists the optional fields a request sets.
func (r Request) fields() []string {
	var out []string
	add := func(set bool, name string) {
		if set {
			out = append(out, name)
		}
	}
	add(r.Service != "", "service")
	add(r.Commit != "", "commit")
	add(r.Command != "", "command")
	add(r.TimeoutSec != 0, "timeoutSec")
	add(r.Runtimes != nil, "runtimes")
	add(r.BuildID != "", "buildId")
	add(r.Artifact != "", "artifact")
	add(r.ReleaseID != "", "releaseId")
	add(r.Start != nil, "start")
	add(r.Runtime != "", "runtime")
	add(r.Migrate != nil, "migrate")
	add(r.KeepReleases != 0, "keepReleases")
	add(r.Publish != nil, "publish")
	add(r.Limits != nil, "limits")
	add(r.Sources != nil, "sources")
	add(r.Mode != "", "mode")
	add(r.BackupID != "", "backupId")
	add(r.ManifestSHA256 != "", "manifestSha256")
	add(r.RestoreUsers, "restoreUsers")
	add(r.Keep != nil, "keep")
	add(r.Route != nil, "route")
	return out
}

// opFields are the fields each operation takes; the others must be absent.
var opFields = map[Op][]string{
	OpBuild:    {"service", "commit", "command", "timeoutSec", "runtimes"},
	OpInstall:  {"service", "buildId", "artifact"},
	OpActivate: {"service", "releaseId", "start", "runtime", "migrate", "keepReleases", "publish", "limits"},
	OpStart:    {"service"},
	OpStop:     {"service"},
	OpRestart:  {"service"},
	OpStatus:   {"service"},
	OpReleases: {"service"},
	OpQuery:    {},
	OpBackup:   {"service", "sources", "mode", "timeoutSec", "runtime"},
	OpRestore:  {"service", "backupId", "manifestSha256", "restoreUsers"},
	OpBackups:  {"service"},
	OpPrune:    {"service", "keep"},
	// ingress
	OpIngressApply:  {"service", "route"},
	OpIngressStatus: {},
}

// Validate checks a request's form; host.json decides what it may name.
func (r Request) Validate() error {
	if r.Version != Version {
		return fmt.Errorf("unsupported protocol version %d", r.Version)
	}
	allowed, known := opFields[r.Op]
	switch {
	case !known:
		return fmt.Errorf("unknown op %q", hostsetup.Printable(string(r.Op), 32))
	case !uuidPattern.MatchString(r.OperationID):
		return errors.New("operationId must be a lower-case UUID")
	}
	for _, f := range r.fields() {
		if !slices.Contains(allowed, f) {
			return fmt.Errorf("%s does not take %s", r.Op, f)
		}
	}
	if r.Op == OpQuery || r.Op == OpIngressStatus {
		return nil
	}
	if err := hostsetup.ValidServiceName(r.Service); err != nil {
		return err
	}
	switch r.Op {
	case OpBuild:
		return r.validateBuild()
	case OpInstall:
		if !idPattern.MatchString(r.BuildID) {
			return errors.New("buildId is not a build ID")
		}
		if !relPath(r.Artifact) {
			return errors.New("artifact must be a clean relative path of letters, digits and ._-/ without \"..\" components")
		}
	case OpActivate:
		return r.validateActivate()
	case OpBackup:
		return r.validateBackup()
	case OpRestore:
		if !backupPattern.MatchString(r.BackupID) {
			return errors.New("backupId is not a backup ID")
		}
		if r.ManifestSHA256 != "" && !sha256Pattern.MatchString(r.ManifestSHA256) {
			return errors.New("manifestSha256 must be a lower-case hex SHA-256")
		}
	case OpIngressApply:
		if r.Route != nil {
			if err := ingress.Join(r.Route.Validate()); err != nil {
				return fmt.Errorf("route: %w", err)
			}
		}
	case OpPrune:
		k := r.Keep
		switch {
		case k == nil:
			return errors.New("keep is required")
		case k.Daily < 0 || k.Daily > MaxKeepDaily || k.Weekly < 0 || k.Weekly > MaxKeepWeekly:
			return fmt.Errorf("keep.daily must be at most %d and keep.weekly at most %d", MaxKeepDaily, MaxKeepWeekly)
		case k.Daily+k.Weekly < 1:
			return errors.New("keep must keep at least one day or week")
		}
	}
	return nil
}

// Bounds of keep.
const (
	MaxKeepDaily  = 366
	MaxKeepWeekly = 260
)

func (r Request) validateBackup() error {
	switch r.Mode {
	case "", ModeOnline, ModeStopped:
	default:
		return fmt.Errorf("mode must be %s or %s", ModeOnline, ModeStopped)
	}
	if r.TimeoutSec < 1 || r.TimeoutSec > MaxTimeoutSec {
		return fmt.Errorf("timeoutSec must be between 1 and %d", MaxTimeoutSec)
	}
	if r.Runtime != "" && !hostsetup.ValidRuntimeName(r.Runtime) {
		return errors.New("runtime is not a runtime name")
	}
	return ValidateSources(r.Sources)
}

// ValidateSources checks the form of backup sources: the fields each type
// takes, clean relative paths, database names and that the paths of file
// and SQLite sources do not overlap. host.json grants nothing about them;
// the data directory is the service's own.
func ValidateSources(sources []BackupSource) error {
	if len(sources) < 1 || len(sources) > MaxSources {
		return fmt.Errorf("sources must list between 1 and %d sources", MaxSources)
	}
	var paths []string
	for i, src := range sources {
		at := fmt.Sprintf("sources[%d]", i)
		allowed := map[string]bool{}
		switch src.Type {
		case SourceFiles, SourceSQLite:
			allowed["path"] = true
			if !relPath(src.Path) || src.Path == "." || hasReservedSuffix(src.Path) {
				return fmt.Errorf("%s.path must be a clean relative path below the data directory, not \".\" (letters, digits and ._-/)", at)
			}
			paths = append(paths, src.Path)
		case SourcePostgres, SourceMongoDB:
			allowed["database"] = true
			if !dbPattern.MatchString(src.Database) {
				return fmt.Errorf("%s.database must be a database name of letters, digits, _ and -", at)
			}
			if src.Type == SourceMongoDB {
				allowed["users"] = true
			}
		case SourceCommand:
			allowed["argv"] = true
			if len(src.Argv) == 0 || !relPath(src.Argv[0]) || src.Argv[0] == "." {
				return fmt.Errorf("%s.argv must begin with a release-relative executable", at)
			}
			if err := validArgs(src.Argv[1:]); err != nil {
				return fmt.Errorf("%s.argv: %w", at, err)
			}
		default:
			return fmt.Errorf("%s.type %q is not a source type", at, hostsetup.Printable(src.Type, 32))
		}
		for name, set := range map[string]bool{"path": src.Path != "", "database": src.Database != "", "users": src.Users, "argv": src.Argv != nil} {
			if set && !allowed[name] {
				return fmt.Errorf("%s: %s does not take %s", at, src.Type, name)
			}
		}
	}
	for i, a := range paths {
		for _, b := range paths[i+1:] {
			if a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/") {
				return fmt.Errorf("the paths %s and %s overlap", a, b)
			}
		}
	}
	return nil
}

// hasReservedSuffix reports a path whose last component ends with the
// suffix of the worker's temporary names.
func hasReservedSuffix(p string) bool { return strings.HasSuffix(path.Base(p), restoreSuffix) }

func (r Request) validateBuild() error {
	switch {
	case !commitPattern.MatchString(r.Commit):
		return errors.New("commit must be a full lower-case commit ID")
	case strings.TrimSpace(r.Command) == "" || len(r.Command) > MaxCommand || strings.ContainsRune(r.Command, 0):
		return fmt.Errorf("command must be a non-empty script of at most %d bytes", MaxCommand)
	case r.TimeoutSec < 1 || r.TimeoutSec > MaxTimeoutSec:
		return fmt.Errorf("timeoutSec must be between 1 and %d", MaxTimeoutSec)
	case len(r.Runtimes) > 8:
		return errors.New("at most 8 runtimes")
	}
	seen := map[string]bool{}
	for _, rt := range r.Runtimes {
		if !hostsetup.ValidRuntimeName(rt) || seen[rt] {
			return fmt.Errorf("runtime %q is not a runtime name, or is listed twice", hostsetup.Printable(rt, 32))
		}
		seen[rt] = true
	}
	return nil
}

func (r Request) validateActivate() error {
	if !idPattern.MatchString(r.ReleaseID) {
		return errors.New("releaseId is not a release ID")
	}
	switch {
	case r.Start == nil:
		// Static content only: no process to start, migrate or limit.
		if r.Publish == nil || r.Migrate != nil || r.Limits != nil {
			return errors.New("an activate without start needs publish and takes neither migrate nor limits")
		}
	case len(r.Start) == 0 || !relPath(r.Start[0]) || r.Start[0] == ".":
		return errors.New("start must begin with a release-relative file")
	}
	if len(r.Start) > 0 {
		if err := validArgs(r.Start[1:]); err != nil {
			return fmt.Errorf("start: %w", err)
		}
	}
	if r.Publish != nil {
		if err := r.Publish.validate(); err != nil {
			return fmt.Errorf("publish: %w", err)
		}
	}
	if r.Limits != nil {
		if err := r.Limits.validate(); err != nil {
			return fmt.Errorf("limits: %w", err)
		}
	}
	if r.Runtime != "" && !hostsetup.ValidRuntimeName(r.Runtime) {
		return errors.New("runtime is not a runtime name")
	}
	if r.Migrate != nil {
		if len(r.Migrate) == 0 || !validProgram(r.Migrate[0]) {
			return errors.New("migrate must begin with a release-relative executable or a runtime program name")
		}
		if err := validArgs(r.Migrate[1:]); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	if r.KeepReleases != 0 && (r.KeepReleases < 2 || r.KeepReleases > MaxKeepReleases) {
		return fmt.Errorf("keepReleases must be between 2 and %d", MaxKeepReleases)
	}
	return nil
}

// Bounds of publish and limits.
const (
	DefaultRetainDays = 14
	MaxRetainDays     = 90
	MaxRetain         = 16
	minUnitMemory     = 1 << 20
	maxUnitMemory     = 16 << 30
	MaxStopTimeoutSec = 600
)

func (p Publish) validate() error {
	if !relPath(p.Dir) {
		return errors.New("dir must be a clean relative path of letters, digits and ._-/ without \"..\" components")
	}
	if len(p.Retain) > MaxRetain {
		return fmt.Errorf("at most %d retain directories", MaxRetain)
	}
	seen := map[string]bool{}
	for _, r := range p.Retain {
		if !retainPrefix(r) || seen[r] {
			return fmt.Errorf("retain %q must be a relative directory ending with \"/\" without components that start with \".\", listed once", hostsetup.Printable(r, 64))
		}
		seen[r] = true
	}
	switch {
	case p.RetainDays != 0 && len(p.Retain) == 0:
		return errors.New("retainDays needs retain")
	case p.RetainDays < 0 || p.RetainDays > MaxRetainDays:
		return fmt.Errorf("retainDays must be between 1 and %d", MaxRetainDays)
	}
	return nil
}

// retainDays returns retainDays with its default.
func (p Publish) retainDays() int {
	if p.RetainDays == 0 {
		return DefaultRetainDays
	}
	return p.RetainDays
}

// retainPrefix accepts a directory such as "assets/": a relative path
// ending with "/" whose components do not start with ".".
func retainPrefix(r string) bool {
	dir, ok := strings.CutSuffix(r, "/")
	if !ok || !relPath(dir) {
		return false
	}
	for _, c := range strings.Split(dir, "/") {
		if strings.HasPrefix(c, ".") {
			return false
		}
	}
	return true
}

var unitSizePattern = regexp.MustCompile(`^([1-9][0-9]{0,8})([KMG])$`)

// unitSize returns the bytes of a size such as 768M; the value is written
// to the unit as it is, so only this form is accepted.
func unitSize(s string) (int64, bool) {
	m := unitSizePattern.FindStringSubmatch(s)
	if m == nil {
		return 0, false
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return 0, false
	}
	return n << map[string]uint{"K": 10, "M": 20, "G": 30}[m[2]], true
}

func (l UnitLimits) validate() error {
	var sizes [2]int64
	for i, v := range []string{l.MemoryHigh, l.MemoryMax} {
		if v == "" {
			continue
		}
		n, ok := unitSize(v)
		if !ok || n < minUnitMemory || n > maxUnitMemory {
			return errors.New("memoryHigh and memoryMax must be sizes such as 768M or 1G between 1M and 16G")
		}
		sizes[i] = n
	}
	if sizes[0] > 0 && sizes[1] > 0 && sizes[0] > sizes[1] {
		return errors.New("memoryHigh must not be larger than memoryMax")
	}
	if l.StopTimeoutSec < 0 || l.StopTimeoutSec > MaxStopTimeoutSec {
		return fmt.Errorf("stopTimeoutSec must be between 1 and %d", MaxStopTimeoutSec)
	}
	return nil
}

// validProgram accepts a runtime program name or a release-relative file.
func validProgram(p string) bool {
	return binPattern.MatchString(p) || relPath(p) && p != "."
}

// Hash identifies a request's content, so that a reused operationId with
// another request is told apart.
func (r Request) Hash() string {
	raw, _ := json.Marshal(r) // a struct of strings, ints and slices always marshals
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// requestKeys are the exact keys of a request; encoding/json alone would
// also accept them in another case.
var requestKeys = map[string]bool{
	"version": true, "op": true, "operationId": true, "service": true, "commit": true, "command": true,
	"timeoutSec": true, "runtimes": true, "buildId": true, "artifact": true, "releaseId": true,
	"start": true, "runtime": true, "migrate": true, "keepReleases": true, "publish": true, "limits": true,
	"sources": true, "mode": true, "backupId": true, "manifestSha256": true, "restoreUsers": true, "keep": true,
	"route": true,
}

// DecodeRequest decodes and validates one request.
func DecodeRequest(raw []byte) (Request, error) {
	var r Request
	if err := decodeStrict(raw, MaxRequest, &r); err != nil {
		return Request{}, err
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return Request{}, err
	}
	for k := range top {
		if !requestKeys[k] {
			return Request{}, fmt.Errorf("unknown key %q", hostsetup.Printable(k, 64))
		}
	}
	return r, r.Validate()
}

// DecodeResponse decodes one reply strictly.
func DecodeResponse(raw []byte) (Response, error) {
	var r Response
	if err := decodeStrict(raw, MaxResponse, &r); err != nil {
		return Response{}, err
	}
	if r.Version != Version {
		return Response{}, fmt.Errorf("unsupported protocol version %d", r.Version)
	}
	switch r.Code {
	case "", CodeRefused, CodeNotFound, CodeConflict, CodeFailed, CodeBusy, CodeDisabled:
	default:
		return Response{}, fmt.Errorf("unknown code %q", hostsetup.Printable(string(r.Code), 32))
	}
	switch r.State {
	case "", StateRunning, StateSucceeded, StateFailed, StateInterrupted:
	default:
		return Response{}, fmt.Errorf("unknown state %q", hostsetup.Printable(string(r.State), 32))
	}
	return r, nil
}

// encodeResponse returns the reply as one line of JSON within MaxResponse.
func encodeResponse(r Response) []byte {
	raw, err := marshal(r)
	if err == nil && len(raw) <= MaxResponse {
		return raw
	}
	if raw, ok := fitBackups(r); ok {
		return raw
	}
	r.Log, r.Releases, r.Backups = "", nil, nil
	r.Message = strings.TrimSpace(r.Message + " (the reply was too large; details were dropped)")
	raw, _ = marshal(r)
	return raw
}

// fitBackups cuts a listing to fit MaxResponse: it omits the sources of the
// entries first, then drops the oldest entries (the listing is newest first),
// and flags the cut. It reports false when even an empty listing does not fit.
func fitBackups(r Response) ([]byte, bool) {
	if len(r.Backups) == 0 {
		return nil, false
	}
	r.BackupsTruncated = true
	list := slices.Clone(r.Backups)
	for i := range list {
		list[i].Sources = nil
	}
	fits := func(n int) ([]byte, bool) {
		r.Backups = list[:n]
		raw, err := marshal(r)
		return raw, err == nil && len(raw) <= MaxResponse
	}
	if raw, ok := fits(len(list)); ok {
		return raw, true
	}
	// Whether n entries fit never improves as n grows.
	lo, hi := 0, len(list)
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if _, ok := fits(mid); ok {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return fits(lo)
}

func marshal(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// decodeStrict decodes one JSON value of at most limit bytes, refusing
// invalid UTF-8, duplicate or unknown keys and trailing data.
func decodeStrict(raw []byte, limit int, dst any) error {
	if len(raw) > limit {
		return fmt.Errorf("message exceeds %d bytes", limit)
	}
	if !utf8.Valid(raw) {
		return errors.New("invalid UTF-8")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	if err := uniqueValue(d); err != nil {
		return err
	}
	if _, err := d.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing data after the JSON value")
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	return d.Decode(dst)
}

func uniqueValue(d *json.Decoder) error {
	tok, err := d.Token()
	if err != nil {
		return err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			tok, err := d.Token()
			if err != nil {
				return err
			}
			key, _ := tok.(string)
			// encoding/json matches keys without regard to case, so keys
			// that differ only in case are duplicates too.
			fold := strings.ToLower(key)
			if seen[fold] {
				return fmt.Errorf("duplicate key %q", hostsetup.Printable(key, 64))
			}
			seen[fold] = true
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
	}
	_, err = d.Token()
	return err
}
