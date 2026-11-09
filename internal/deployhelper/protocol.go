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
//	activate  service, releaseId, start, runtime (optional), migrate (optional),
//	          keepReleases (optional, 2-20, default 3)
//	start     service
//	stop      service
//	restart   service
//	status    service
//	releases  service
//	query     (operationId names the operation to look up)
//	backup, restore, backups: reserved for a later version, always refused
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
// components; arguments also allow "=:,@+".
//
// A reply carries "version", "op" and "operationId" of the request, and:
//
//   - "state" for the operations that change something (build, install,
//     activate, start, stop, restart) and for query: "succeeded", "failed",
//     "running" (query only: the operation is still in progress) or
//     "interrupted" (query only: the helper stopped before it finished; what
//     it did is unknown and must be inspected on the host).
//   - "code" and "message" when the request was not carried out or failed:
//     "refused" (invalid request, or not allowed by host.json or the host's
//     state), "not_found" (unknown build, release, unit or operation),
//     "conflict" (the operationId was used for another request), "busy"
//     (another operation of the service is running; nothing was done) or
//     "failed" (the operation started and failed). message is English text
//     with control characters replaced.
//   - "log": the sanitized tail (at most MaxLog bytes) of the output of the
//     build, migration or systemctl command.
//   - one result object: "build" {buildId, commit}, "install" {releaseId,
//     manifest}, "activate" {releaseId, previous, switched, unitChanged},
//     "unit" (status) {loadState, activeState, subState, unitFileState,
//     mainPid, release} or "releases" [{releaseId, current, previous,
//     manifest}].
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
// are removed 90 days after they finished; status, releases and query are
// not recorded.
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
// build copies the lcm-owned checkout /var/lib/lcm/work/<service>, whose
// HEAD (read from .git without running git as root) must be the commit,
// into /var/lib/lcm-build/<service>/<buildId>/src owned by the service's
// own build account lcmb-<service>, and runs the command there with
// systemd-run as that account in a sandbox (see buildArgs). It keeps the last 3 builds of a service. install copies the
// artifact directory of a successful build to
// /opt/<service>/releases/<releaseId>, root-owned, directories 0755 and
// files 0644 or 0755, refusing links that leave it, and writes the
// manifest next to it as <releaseId>.json. activate runs the optional
// migration as the service account in the new release, writes
// /etc/systemd/system/<service>.service from the built-in template,
// switches the /opt/<service>/current and previous links, reloads systemd
// when the unit changed, enables and restarts the unit, and prunes releases
// beyond keepReleases, never the current or previous one. start, stop,
// restart and status act on <service>.service only, and only on a unit the
// helper wrote.
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
	"strings"
	"unicode/utf8"

	"github.com/ACaccel/lcm/internal/hostsetup"
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
	// request does not say.
	DefaultKeepReleases = 3
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
	// Reserved for typed backups.
	OpBackup  Op = "backup"
	OpRestore Op = "restore"
	OpBackups Op = "backups"
)

// Changes reports whether op changes the host and is recorded in the
// ledger.
func (op Op) Changes() bool {
	switch op {
	case OpBuild, OpInstall, OpActivate, OpStart, OpStop, OpRestart:
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
	ReleaseID    string   `json:"releaseId,omitempty"`
	Start        []string `json:"start,omitempty"`
	Runtime      string   `json:"runtime,omitempty"`
	Migrate      []string `json:"migrate,omitempty"`
	KeepReleases int      `json:"keepReleases,omitempty"`
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

var (
	uuidPattern   = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	commitPattern = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
	idPattern     = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}Z-[0-9a-f]{8}$`)
	relPattern    = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,256}$`)
	argPattern    = regexp.MustCompile(`^[A-Za-z0-9._/=:,@+-]{1,256}$`)
	binPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$`)
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
	return out
}

// opFields are the fields each operation takes; the others must be absent.
var opFields = map[Op][]string{
	OpBuild:    {"service", "commit", "command", "timeoutSec", "runtimes"},
	OpInstall:  {"service", "buildId", "artifact"},
	OpActivate: {"service", "releaseId", "start", "runtime", "migrate", "keepReleases"},
	OpStart:    {"service"},
	OpStop:     {"service"},
	OpRestart:  {"service"},
	OpStatus:   {"service"},
	OpReleases: {"service"},
	OpQuery:    {},
	OpBackup:   nil,
	OpRestore:  nil,
	OpBackups:  nil,
}

// ErrReserved refuses the operations reserved for a later version.
var ErrReserved = errors.New("this operation is reserved for a later version")

// Validate checks a request's form; host.json decides what it may name.
func (r Request) Validate() error {
	if r.Version != Version {
		return fmt.Errorf("unsupported protocol version %d", r.Version)
	}
	allowed, known := opFields[r.Op]
	switch {
	case !known:
		return fmt.Errorf("unknown op %q", hostsetup.Printable(string(r.Op), 32))
	case allowed == nil:
		return ErrReserved
	case !uuidPattern.MatchString(r.OperationID):
		return errors.New("operationId must be a lower-case UUID")
	}
	for _, f := range r.fields() {
		if !slices.Contains(allowed, f) {
			return fmt.Errorf("%s does not take %s", r.Op, f)
		}
	}
	if r.Op == OpQuery {
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
	}
	return nil
}

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
	if len(r.Start) == 0 || !relPath(r.Start[0]) || r.Start[0] == "." {
		return errors.New("start must begin with a release-relative file")
	}
	if err := validArgs(r.Start[1:]); err != nil {
		return fmt.Errorf("start: %w", err)
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
	if r.KeepReleases != 0 && (r.KeepReleases < 2 || r.KeepReleases > 20) {
		return errors.New("keepReleases must be between 2 and 20")
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
	"start": true, "runtime": true, "migrate": true, "keepReleases": true,
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
	case "", CodeRefused, CodeNotFound, CodeConflict, CodeFailed, CodeBusy:
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
	r.Log, r.Releases = "", nil
	r.Message = strings.TrimSpace(r.Message + " (the reply was too large; details were dropped)")
	raw, _ = marshal(r)
	return raw
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
