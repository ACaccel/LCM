package deployhelper

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	_ "modernc.org/sqlite" // the driver that copies SQLite databases consistently

	"github.com/ACaccel/lcm/internal/hostsetup"
)

// The backup worker is "lcm backup-worker <in> <out>", started by the
// helper with systemd-run as the service account in a sandbox. It is not
// privileged: it reads or writes the service's own data directory, which
// the service could do itself, and nothing else. It never runs as root.
// The helper treats everything it leaves in <out> as untrusted input.

// Names inside the staging directories.
const (
	specName   = "spec.json"
	resultName = "result.json"
	// restoreSuffix names the copy that a restore builds next to its target
	// before swapping it in.
	restoreSuffix = ".lcm-new"
	maxSpec       = 1 << 20
	maxResult     = 1 << 20
	// restoredMode is the permission of a restored database when neither the
	// file it replaces nor the set says which to use.
	restoredMode fs.FileMode = 0o640
	// sqliteHeader starts every SQLite database file.
	sqliteHeader = "SQLite format 3\x00"
)

// artifactRef is a staged artifact a restore reads, with the SHA-256 it is
// verified against again before use.
type artifactRef struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// specSource is a source as the worker sees it. For a restore, Artifacts
// are the files of <in>, or Absent says that the path did not exist.
type specSource struct {
	BackupSource
	Artifacts []artifactRef `json:"artifacts,omitempty"`
	Absent    bool          `json:"absent,omitempty"`
	// Mode is the permission of the database file when the set was made, the
	// fallback of a restore when nothing is there to keep the mode of.
	Mode uint32 `json:"mode,omitempty"`
}

// workerSpec is <in>/spec.json, written by the helper.
type workerSpec struct {
	Version int `json:"version"`
	// Mode is "backup" or "restore"; "check" and "preflight" run before
	// anything is stopped: "preflight" refuses a MONGODB_URI the sources
	// cannot use, "check" also reads the users of a set.
	Mode    string       `json:"mode"`
	Service string       `json:"service"`
	Sources []specSource `json:"sources"`
	// MaxBytes and MaxEntries bound what the worker writes or extracts.
	MaxBytes   int64 `json:"maxBytes"`
	MaxEntries int   `json:"maxEntries"`
	// AllowMissing lets a source whose path does not exist count as absent
	// instead of failing: the copy of what a restore replaces.
	AllowMissing bool `json:"allowMissing,omitempty"`
	// Tools maps the database tools the sources need to the absolute paths
	// the helper verified as root; the worker runs nothing it finds on its
	// own.
	Tools map[string]string `json:"tools,omitempty"`
}

// sourceResult is what the worker reports for a source.
type sourceResult struct {
	Absent  bool `json:"absent,omitempty"`
	Skipped int  `json:"skipped,omitempty"`
	// Mode is the permission bits of a SQLite database file, without
	// special bits.
	Mode uint32 `json:"mode,omitempty"`
}

// workerResult is <out>/result.json.
type workerResult struct {
	Version int            `json:"version"`
	Sources []sourceResult `json:"sources"`
}

// WorkerHost maps the worker's fixed locations, so that tests can use a
// directory tree.
type WorkerHost struct {
	// DataDir is the service's data directory, ReleaseDir the directory of
	// its current release.
	DataDir    func(service string) string
	ReleaseDir func(service string) string
	// ToolPath maps the host path of a database tool, which the helper chose
	// and put in the specification, to the path to run; nil keeps it.
	ToolPath func(hostPath string) string
}

// DefaultWorkerHost is the host layout the helper's units use.
func DefaultWorkerHost() WorkerHost {
	return WorkerHost{
		DataDir:    hostsetup.ServiceDataDir,
		ReleaseDir: func(service string) string { return hostsetup.ServiceOptDir(service) + "/current" },
	}
}

// worker is one run of the backup worker.
type worker struct {
	host WorkerHost
	spec workerSpec
	in   string
	out  *os.Root
	data *os.Root
	log  io.Writer
	// written counts the bytes written to artifacts or the data directory.
	written int64
	entries int
}

// RunWorker carries out the specification in inDir, writing its artifacts
// to outDir (backup) or the data directory (restore). Progress goes to
// log.
func RunWorker(ctx context.Context, host WorkerHost, inDir, outDir string, log io.Writer) error {
	if os.Geteuid() == 0 {
		return errors.New("the backup worker must not run as root")
	}
	spec, err := readSpec(inDir)
	if err != nil {
		return err
	}
	out, err := os.OpenRoot(outDir)
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()
	data, err := os.OpenRoot(host.DataDir(spec.Service))
	if err != nil {
		return fmt.Errorf("open the data directory: %w", err)
	}
	defer func() { _ = data.Close() }()
	w := &worker{host: host, spec: spec, in: inDir, out: out, data: data, log: log}
	switch spec.Mode {
	case "restore":
		return w.restore(ctx)
	case "check":
		return w.checkUsers(ctx)
	case "preflight":
		return w.checkURIs()
	}
	return w.backup(ctx)
}

func readSpec(inDir string) (workerSpec, error) {
	in, err := os.OpenRoot(inDir)
	if err != nil {
		return workerSpec{}, err
	}
	defer func() { _ = in.Close() }()
	f, err := in.Open(specName)
	if err != nil {
		return workerSpec{}, err
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, maxSpec+1))
	if err != nil {
		return workerSpec{}, err
	}
	var spec workerSpec
	if err := decodeStrict(raw, maxSpec, &spec); err != nil {
		return spec, fmt.Errorf("specification: %w", err)
	}
	switch {
	case spec.Version != Version || (spec.Mode != "backup" && spec.Mode != "restore" && spec.Mode != "check" && spec.Mode != "preflight"):
		return spec, errors.New("specification: unsupported version or mode")
	case hostsetup.ValidServiceName(spec.Service) != nil:
		return spec, errors.New("specification: invalid service")
	case spec.MaxBytes < 1 || spec.MaxEntries < 1:
		return spec, errors.New("specification: no limits")
	}
	plain := make([]BackupSource, len(spec.Sources))
	for i, s := range spec.Sources {
		plain[i] = s.BackupSource
	}
	if err := ValidateSources(plain); err != nil {
		return spec, fmt.Errorf("specification: %w", err)
	}
	return spec, nil
}

func (w *worker) logf(format string, args ...any) {
	_, _ = fmt.Fprintf(w.log, format+"\n", args...)
}

// charge accounts for n written bytes.
func (w *worker) charge(n int64) error {
	if w.written += n; w.written > w.spec.MaxBytes {
		return fmt.Errorf("more than %d bytes", w.spec.MaxBytes)
	}
	return nil
}

func (w *worker) entry() error {
	if w.entries++; w.entries > w.spec.MaxEntries {
		return fmt.Errorf("more than %d entries", w.spec.MaxEntries)
	}
	return nil
}

// limitedWriter charges what passes through it.
type limitedWriter struct {
	w io.Writer
	c *worker
}

func (l limitedWriter) Write(p []byte) (int, error) {
	if err := l.c.charge(int64(len(p))); err != nil {
		return 0, err
	}
	return l.w.Write(p)
}

// ---- backup ----

func (w *worker) backup(ctx context.Context) error {
	res := workerResult{Version: Version}
	for i, src := range w.spec.Sources {
		if err := ctx.Err(); err != nil {
			return err
		}
		var r sourceResult
		var err error
		switch src.Type {
		case SourceFiles:
			r, err = w.backupFiles(i, src.BackupSource)
		case SourceSQLite:
			r, err = w.backupSQLite(i, src.BackupSource)
		case SourcePostgres:
			err = w.dump(ctx, "pg_dump", []string{"--format=custom", "--no-password", "--file=" + w.outPath(artifactNames(i, src.BackupSource)[0]), "--dbname=" + src.Database})
		case SourceMongoDB:
			err = w.backupMongo(ctx, i, src.BackupSource)
		case SourceCommand:
			err = w.backupCommand(ctx, i, src.BackupSource)
		}
		if err != nil {
			return fmt.Errorf("source %d (%s): %w", i, describeSource(src.BackupSource), err)
		}
		res.Sources = append(res.Sources, r)
	}
	raw, err := json.Marshal(res)
	if err != nil {
		return err
	}
	return w.out.WriteFile(resultName, raw, 0o600)
}

// outPath is the real path of an artifact in the output directory, for
// the external tools that write it themselves.
func (w *worker) outPath(name string) string { return w.out.Name() + "/" + name }

func (w *worker) create(name string) (*os.File, error) {
	return w.out.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
}

// checkName refuses names that a tar archive, a log or a terminal could
// misrepresent.
func checkName(name string) error {
	if !utf8.ValidString(name) || strings.IndexFunc(name, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) >= 0 {
		return fmt.Errorf("the name %q is not printable", hostsetup.Printable(name, 80))
	}
	return nil
}

func (w *worker) backupFiles(i int, src BackupSource) (sourceResult, error) {
	var res sourceResult
	info, err := w.data.Lstat(src.Path)
	switch {
	case errors.Is(err, fs.ErrNotExist) && w.spec.AllowMissing:
		return sourceResult{Absent: true}, nil
	case err != nil:
		return res, err
	case info.Mode()&fs.ModeSymlink != 0:
		return res, errors.New("the path is a symbolic link")
	}
	f, err := w.create(artifactNames(i, src)[0])
	if err != nil {
		return res, err
	}
	bw := bufio.NewWriter(limitedWriter{w: f, c: w})
	tw := tar.NewWriter(bw)
	err = w.addTree(tw, src.Path, info, &res)
	if err == nil {
		err = tw.Close()
	}
	if err == nil {
		err = bw.Flush()
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		w.logf("files %s: %d entries, %d skipped", src.Path, w.entries, res.Skipped)
	}
	return res, err
}

// archivedMode keeps the owner's bits and the group's and others' read and
// execute bits.
func archivedMode(m fs.FileMode) int64 { return int64(m.Perm() & 0o755) }

func (w *worker) addTree(tw *tar.Writer, rel string, info fs.FileInfo, res *sourceResult) error {
	if err := w.entry(); err != nil {
		return err
	}
	if err := checkName(rel); err != nil {
		return err
	}
	switch {
	case info.IsDir():
		if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: rel + "/", Mode: archivedMode(info.Mode()) | 0o700, ModTime: info.ModTime()}); err != nil {
			return err
		}
		d, err := w.data.Open(rel)
		if err != nil {
			return err
		}
		names, err := d.Readdirnames(-1)
		_ = d.Close()
		if err != nil {
			return err
		}
		slices.Sort(names)
		for _, n := range names {
			child := path.Join(rel, n)
			ci, err := w.data.Lstat(child)
			if errors.Is(err, fs.ErrNotExist) {
				continue // removed while the backup ran
			}
			if err != nil {
				return err
			}
			if err := w.addTree(tw, child, ci, res); err != nil {
				return err
			}
		}
		return nil
	case info.Mode().IsRegular():
		f, err := w.data.Open(rel)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		fi, err := f.Stat()
		if err != nil {
			return err
		}
		if !fi.Mode().IsRegular() {
			res.Skipped++
			return nil
		}
		if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: rel, Mode: archivedMode(fi.Mode()), Size: fi.Size(), ModTime: fi.ModTime()}); err != nil {
			return err
		}
		if _, err := io.CopyN(tw, f, fi.Size()); err != nil {
			return fmt.Errorf("%s changed while it was read: %w", rel, err)
		}
		return nil
	}
	// Links, devices, sockets and pipes are not stored.
	res.Skipped++
	return nil
}

// noLinks refuses a path with a symbolic link in any component.
func (w *worker) noLinks(rel string) error {
	parts := strings.Split(rel, "/")
	for i := range parts {
		info, err := w.data.Lstat(strings.Join(parts[:i+1], "/"))
		if err != nil {
			return err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symbolic link", strings.Join(parts[:i+1], "/"))
		}
	}
	return nil
}

func (w *worker) backupSQLite(i int, src BackupSource) (sourceResult, error) {
	if err := w.noLinks(src.Path); err != nil {
		if errors.Is(err, fs.ErrNotExist) && w.spec.AllowMissing {
			return sourceResult{Absent: true}, nil
		}
		return sourceResult{}, err
	}
	info, err := w.data.Lstat(src.Path)
	if err != nil {
		return sourceResult{}, err
	}
	if !info.Mode().IsRegular() {
		return sourceResult{}, errors.New("the path is not a regular file")
	}
	dst := w.outPath(artifactNames(i, src)[0])
	db, err := sql.Open("sqlite", "file:"+w.data.Name()+"/"+src.Path+"?mode=ro&_pragma=busy_timeout(30000)")
	if err != nil {
		return sourceResult{}, err
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("VACUUM INTO ?", dst); err != nil {
		return sourceResult{}, fmt.Errorf("copy the database: %w", err)
	}
	if err := w.checkSQLite(dst); err != nil {
		return sourceResult{}, err
	}
	fi, err := w.out.Lstat(artifactNames(i, src)[0])
	if err != nil {
		return sourceResult{}, err
	}
	if err := w.charge(fi.Size()); err != nil {
		return sourceResult{}, err
	}
	w.logf("sqlite %s: %d bytes", src.Path, fi.Size())
	return sourceResult{Mode: uint32(info.Mode().Perm())}, nil
}

// checkSQLite opens a copy and runs the quick integrity check.
func (w *worker) checkSQLite(file string) error {
	db, err := sql.Open("sqlite", "file:"+file+"?mode=ro")
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	var verdict string
	if err := db.QueryRow("PRAGMA quick_check").Scan(&verdict); err != nil {
		return fmt.Errorf("check the copy: %w", err)
	}
	if verdict != "ok" {
		return fmt.Errorf("the copy failed its integrity check: %s", hostsetup.Printable(verdict, 200))
	}
	return nil
}

func (w *worker) backupMongo(ctx context.Context, i int, src BackupSource) error {
	if err := checkURIPath(src); err != nil {
		return err
	}
	conf, cleanup, err := mongoConfig()
	if err != nil {
		return err
	}
	defer cleanup()
	names := artifactNames(i, src)
	// Without --db, mongodump dumps every database but local, admin
	// included.
	var only []string
	if !src.All {
		only = []string{"--db=" + src.Database}
	}
	if err := w.dump(ctx, "mongodump", mongoArgs(conf, w.outPath(names[0]), only...)); err != nil {
		return err
	}
	if !src.Users {
		return nil
	}
	// The whole admin database: users and roles restore only together with
	// admin.system.version, which tells mongorestore their auth schema.
	if err := w.dump(ctx, "mongodump", mongoArgs(conf, w.outPath(names[1]), "--db=admin")); err != nil {
		return err
	}
	// The users once more as plain BSON, which a restore checks with
	// bsondump before it replaces any account.
	f, err := w.create(names[2])
	if err != nil {
		return err
	}
	args := []string{"--db=admin", "--collection=system.users", "--out=-"}
	if conf != "" {
		args = append(args, "--config="+conf)
	}
	err = w.run(ctx, "mongodump", args, limitedWriter{w: f, c: w}, "")
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// mongoURI returns the connection string of the service's environment.
func mongoURI() string { return os.Getenv("MONGODB_URI") }

// uriParts returns the database path of a MongoDB connection string, its
// user name and the database that user authenticates against
// (authSource, else the path, else admin). A string without credentials
// has no user.
func uriParts(uri string) (database, user, authDB string, err error) {
	rest, ok := strings.CutPrefix(uri, "mongodb://")
	if !ok {
		if rest, ok = strings.CutPrefix(uri, "mongodb+srv://"); !ok {
			return "", "", "", errors.New("MONGODB_URI is not a mongodb:// or mongodb+srv:// connection string")
		}
	}
	rest, query, _ := strings.Cut(rest, "?")
	authority, path, _ := strings.Cut(rest, "/")
	if database, err = url.PathUnescape(path); err != nil {
		return "", "", "", errors.New("MONGODB_URI has an invalid database path")
	}
	if at := strings.LastIndex(authority, "@"); at >= 0 {
		name, _, _ := strings.Cut(authority[:at], ":")
		if user, err = url.PathUnescape(name); err != nil {
			return "", "", "", errors.New("MONGODB_URI has an invalid user name")
		}
	}
	values, err := url.ParseQuery(query)
	if err != nil {
		return "", "", "", errors.New("MONGODB_URI has invalid options")
	}
	authDB = values.Get("authSource")
	switch {
	case authDB != "":
	case database != "":
		authDB = database
	default:
		authDB = "admin"
	}
	return database, user, authDB, nil
}

// checkURIPath refuses a connection string with a database path for a
// source of all databases or one with users: the MongoDB tools take the
// path as --db, so they would back up or restore that database alone
// without an error, or refuse the admin database the users live in.
func checkURIPath(src BackupSource) error {
	uri := mongoURI()
	if (!src.All && !src.Users) || uri == "" {
		return nil
	}
	database, _, authDB, err := uriParts(uri)
	if err != nil {
		return err
	}
	if database != "" {
		return fmt.Errorf("MONGODB_URI names the database %q, so the tools would only see that database; for a source of all databases or with users "+
			"leave the path empty and name the user's database with authSource, such as mongodb://user:password@host:27017/?authSource=%s", database, authDB)
	}
	return nil
}

// mongoArgs builds the arguments of a MongoDB tool writing to archive; conf,
// when set, is the private config file that holds the connection string.
func mongoArgs(conf, archive string, args ...string) []string {
	out := append([]string{"--archive=" + archive, "--gzip"}, args...)
	if conf != "" {
		out = append(out, "--config="+conf)
	}
	return out
}

// mongoConfig writes the connection string that the service's environment
// names, if any, to a 0600 YAML file in a private directory of the worker's
// own temporary file system, and returns the file and a function that
// removes it. The string holds a password, so it must not appear in the
// arguments of the tools, which any process of the account can read in /proc.
func mongoConfig() (string, func(), error) {
	uri := os.Getenv("MONGODB_URI")
	if uri == "" {
		return "", func() {}, nil
	}
	dir, err := os.MkdirTemp("", "lcm-mongo-")
	if err != nil {
		return "", nil, fmt.Errorf("prepare the MongoDB connection settings: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	quoted, err := json.Marshal(uri) // a JSON string is a valid YAML double-quoted scalar
	if err != nil {
		cleanup()
		return "", nil, err
	}
	file := dir + "/config.yaml"
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // a new file in the private directory just made
	if err == nil {
		_, err = fmt.Fprintf(f, "uri: %s\n", quoted)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}
	if err != nil {
		cleanup()
		return "", nil, fmt.Errorf("write the MongoDB connection settings: %w", err)
	}
	return file, cleanup, nil
}

// dump runs a database tool that writes its artifact itself, then accounts
// for its size.
func (w *worker) dump(ctx context.Context, tool string, args []string) error {
	if err := w.run(ctx, tool, args, nil, ""); err != nil {
		return err
	}
	for _, a := range args {
		for _, p := range []string{"--file=", "--archive="} {
			if name, ok := strings.CutPrefix(a, p); ok {
				fi, err := os.Lstat(name)
				if err != nil {
					return err
				}
				return w.charge(fi.Size())
			}
		}
	}
	return nil
}

// toolPath returns the path the helper gave for a database tool.
func (w *worker) toolPath(name string) (string, error) {
	p := w.spec.Tools[name]
	if p == "" || !path.IsAbs(p) || path.Clean(p) != p {
		return "", fmt.Errorf("the helper gave no usable path for %s", name)
	}
	if w.host.ToolPath != nil {
		p = w.host.ToolPath(p)
	}
	return p, nil
}

// run executes an external tool of the host. stdout, when set, receives its
// standard output; its standard error goes to the log.
func (w *worker) run(ctx context.Context, tool string, args []string, stdout io.Writer, dir string) error {
	p := tool
	if !strings.HasPrefix(tool, "/") {
		var err error
		if p, err = w.toolPath(tool); err != nil {
			return err
		}
	}
	cmd := exec.CommandContext(ctx, p, args...) //nolint:gosec // a fixed tool or the release's own program, run as the service account
	cmd.Env = toolEnv(path.Base(p), os.Environ())
	cmd.Dir = "/"
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Stdout = stdout
	cmd.Stderr = w.log
	cmd.WaitDelay = 10 * time.Second
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s failed: %w", path.Base(p), err)
	}
	return nil
}

// toolEnv is the environment of a program the worker starts. The MongoDB
// tools get their connection string from the private --config file, so
// MONGODB_URI is left out of theirs: unlike the worker, which is not
// dumpable, a tool's environment is readable by every process of the
// account through /proc.
func toolEnv(tool string, env []string) []string {
	if tool != "mongodump" && tool != "mongorestore" {
		return env
	}
	return slices.DeleteFunc(slices.Clone(env), func(kv string) bool {
		name, _, _ := strings.Cut(kv, "=")
		return name == "MONGODB_URI"
	})
}

func (w *worker) backupCommand(ctx context.Context, i int, src BackupSource) error {
	rel := w.host.ReleaseDir(w.spec.Service)
	release, err := os.OpenRoot(rel)
	if err != nil {
		return fmt.Errorf("the service has no current release: %w", err)
	}
	defer func() { _ = release.Close() }()
	info, err := release.Stat(src.Argv[0])
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("%s is not an executable file of the release", src.Argv[0])
	}
	f, err := w.create(artifactNames(i, src)[0])
	if err != nil {
		return err
	}
	err = w.run(ctx, rel+"/"+src.Argv[0], src.Argv[1:], limitedWriter{w: f, c: w}, rel)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// ---- restore ----

func (w *worker) restore(ctx context.Context) error {
	in, err := os.OpenRoot(w.in)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	for i, src := range w.spec.Sources {
		if err := ctx.Err(); err != nil {
			return err
		}
		if src.Type == SourceCommand {
			continue
		}
		for _, a := range src.Artifacts {
			if err := verifyArtifact(in, a); err != nil {
				return fmt.Errorf("source %d (%s): %w", i, describeSource(src.BackupSource), err)
			}
		}
		if err := w.restoreSource(ctx, in, i, src); err != nil {
			return fmt.Errorf("source %d (%s): %w", i, describeSource(src.BackupSource), err)
		}
	}
	return nil
}

// checkURIs refuses a MongoDB connection string that the MongoDB sources
// cannot use. It runs again inside the backup and restore workers, but a
// run before anything is stopped keeps a bad string from stopping and
// restarting the service for nothing.
func (w *worker) checkURIs() error {
	for i, src := range w.spec.Sources {
		if src.Type != SourceMongoDB {
			continue
		}
		if err := checkURIPath(src.BackupSource); err != nil {
			return fmt.Errorf("source %d (%s): %w", i, describeSource(src.BackupSource), err)
		}
	}
	return nil
}

// checkUsers runs before a restore replaces anything. It applies the checks
// of the connection string, and for a restore of MongoDB users the users of
// the set must include the one the connection string logs in as, because
// mongorestore removes every user the set does not hold, and the backups
// and the undo of this very restore need that user.
func (w *worker) checkUsers(ctx context.Context) error {
	if err := w.checkURIs(); err != nil {
		return err
	}
	in, err := os.OpenRoot(w.in)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	uri := mongoURI()
	if uri == "" {
		return nil
	}
	_, user, authDB, err := uriParts(uri)
	if err != nil || user == "" {
		return err
	}
	for _, src := range w.spec.Sources {
		if src.Type != SourceMongoDB || !src.Users {
			continue
		}
		i := slices.IndexFunc(src.Artifacts, func(a artifactRef) bool { return strings.HasSuffix(a.Name, "-users.bson") })
		if i < 0 {
			return errors.New("the set has no copy of the users to check")
		}
		a := src.Artifacts[i]
		if err := verifyArtifact(in, a); err != nil {
			return err
		}
		var out bytes.Buffer
		if err := w.run(ctx, "bsondump", []string{"--quiet", w.in + "/" + a.Name}, &limitedBuffer{b: &out, max: maxUsersDump}, ""); err != nil {
			return err
		}
		found := false
		for line := range strings.SplitSeq(out.String(), "\n") {
			var u struct {
				User string `json:"user"`
				DB   string `json:"db"`
			}
			if json.Unmarshal([]byte(line), &u) == nil && u.User == user && u.DB == authDB {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("the set holds no user %s of the database %s, which MONGODB_URI logs in as: restoring its users would remove that user, "+
				"and the backups and the undo of this restore would fail to log in; restore without restoreUsers, or from a set that has the user", user, authDB)
		}
	}
	return nil
}

// maxUsersDump bounds what bsondump may print for the users of a set.
const maxUsersDump = 64 << 20

// limitedBuffer collects output up to max bytes and refuses the rest.
type limitedBuffer struct {
	b   *bytes.Buffer
	max int
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if l.b.Len()+len(p) > l.max {
		return 0, errors.New("the users of the set exceed the size a check reads")
	}
	return l.b.Write(p)
}

// verifyArtifact checks a staged artifact against the hash it was given.
func verifyArtifact(in *os.Root, a artifactRef) error {
	f, err := in.Open(a.Name)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return err
	}
	if n != a.Size || hex.EncodeToString(h.Sum(nil)) != a.SHA256 {
		return fmt.Errorf("%s does not match its manifest", a.Name)
	}
	return nil
}

func (w *worker) restoreSource(ctx context.Context, in *os.Root, i int, src specSource) error {
	switch src.Type {
	case SourceFiles, SourceSQLite:
		if src.Absent {
			return w.removePath(src.Path)
		}
		if len(src.Artifacts) != 1 {
			return errors.New("the set has no artifact for it")
		}
		if src.Type == SourceFiles {
			return w.extract(in, src.Path, src.Artifacts[0].Name)
		}
		return w.restoreSQLite(in, src.Path, src.Artifacts[0].Name, fs.FileMode(src.Mode))
	case SourcePostgres:
		if len(src.Artifacts) != 1 {
			return errors.New("the set has no artifact for it")
		}
		return w.run(ctx, "pg_restore", []string{"--clean", "--if-exists", "--no-owner", "--no-privileges", "--exit-on-error", "--no-password",
			"--dbname=" + src.Database, w.in + "/" + src.Artifacts[0].Name}, nil, "")
	case SourceMongoDB:
		return w.restoreMongo(ctx, src)
	}
	return nil
}

func (w *worker) restoreMongo(ctx context.Context, src specSource) error {
	if len(src.Artifacts) == 0 {
		return errors.New("the set has no artifact for it")
	}
	if err := checkURIPath(src.BackupSource); err != nil {
		return err
	}
	conf, cleanup, err := mongoConfig()
	if err != nil {
		return err
	}
	defer cleanup()
	for k, a := range src.Artifacts {
		ns := []string{"--nsInclude=" + src.Database + ".*"}
		switch {
		case k > 0 && strings.HasSuffix(a.Name, "-users.bson"):
			// Only read by the check before the restore.
			continue
		case k > 0 && strings.HasSuffix(a.Name, "-admin.archive"):
			ns = []string{"--nsInclude=admin.system.users", "--nsInclude=admin.system.roles", "--nsInclude=admin.system.version"}
		case src.All:
			// Every database of the set but the instance's own: users and
			// roles come back only from their artifacts, when asked.
			ns = []string{"--nsExclude=admin.*", "--nsExclude=config.*", "--nsExclude=local.*"}
		}
		args := mongoArgs(conf, w.in+"/"+a.Name, append([]string{"--drop"}, ns...)...)
		if err := w.run(ctx, "mongorestore", args, nil, ""); err != nil {
			return err
		}
	}
	return nil
}

// removePath removes a path that did not exist when the set was made. The
// removal does not follow links.
func (w *worker) removePath(rel string) error {
	if err := w.data.RemoveAll(rel); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	w.logf("removed %s: it did not exist in the backup", rel)
	return nil
}

// restoreSQLite copies a staged database next to its target and swaps it
// in, removing the journal files of the replaced database first: they
// belong to it and would corrupt the restored one.
//
// The restored file keeps the permissions of the file it replaces; when none
// exists it gets the permissions recorded at backup time, else restoredMode.
// Special bits are never set.
func (w *worker) restoreSQLite(in *os.Root, rel, artifact string, recorded fs.FileMode) error {
	src, err := in.Open(artifact)
	if err != nil {
		return err
	}
	defer func() { _ = src.Close() }()
	head := make([]byte, len(sqliteHeader))
	if _, err := io.ReadFull(src, head); err != nil || string(head) != sqliteHeader {
		return errors.New("the artifact is not a SQLite database")
	}
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := w.data.MkdirAll(path.Dir(rel), 0o750); err != nil {
		return err
	}
	mode := restoredMode
	if recorded&0o600 == 0o600 && recorded&^0o777 == 0 {
		mode = recorded
	}
	if old, err := w.data.Lstat(rel); err == nil && old.Mode().IsRegular() {
		mode = old.Mode().Perm() | 0o600
	}
	tmp := rel + restoreSuffix
	if err := w.data.RemoveAll(tmp); err != nil {
		return err
	}
	dst, err := w.data.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = io.Copy(limitedWriter{w: dst, c: w}, src)
	if err == nil {
		err = dst.Chmod(mode)
	}
	if err == nil {
		err = dst.Sync()
	}
	if cerr := dst.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = w.data.RemoveAll(tmp)
		return err
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if err := w.data.RemoveAll(rel + suffix); err != nil {
			_ = w.data.RemoveAll(tmp)
			return err
		}
	}
	if err := w.data.RemoveAll(rel); err != nil {
		_ = w.data.RemoveAll(tmp)
		return err
	}
	if err := w.data.Rename(tmp, rel); err != nil {
		return err
	}
	w.logf("sqlite %s restored", rel)
	return nil
}

// within reports whether name is target or below it.
func within(name, target string) bool {
	return name == target || strings.HasPrefix(name, target+"/")
}

// extract builds the tree of an archive next to target and swaps it in.
// Every name must be target or below it, clean, relative and without
// "..", and every entry a regular file or a directory with no special mode
// bits; anything else refuses the archive before the target is touched.
func (w *worker) extract(in *os.Root, target, artifact string) error {
	f, err := in.Open(artifact)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	if err := w.data.MkdirAll(path.Dir(target), 0o750); err != nil {
		return err
	}
	// The copy lives beside its target as <target>.lcm-new; names inside
	// the archive are mapped into it.
	stage := target + restoreSuffix
	if err := w.data.RemoveAll(stage); err != nil {
		return err
	}
	tr := tar.NewReader(f)
	type dirMode struct {
		name string
		mode fs.FileMode
		mod  time.Time
	}
	var dirs []dirMode
	seen := map[string]bool{}
	fail := func(err error) error {
		_ = w.data.RemoveAll(stage)
		return err
	}
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fail(fmt.Errorf("read the archive: %w", err))
		}
		name := strings.TrimSuffix(hdr.Name, "/")
		if hdr.Typeflag == tar.TypeDir && hdr.Name != name+"/" {
			return fail(fmt.Errorf("directory entry %q is malformed", hostsetup.Printable(hdr.Name, 80)))
		}
		if err := checkEntryName(name, target); err != nil {
			return fail(err)
		}
		if hdr.Mode&^0o777 != 0 {
			return fail(fmt.Errorf("%s has special mode bits", name))
		}
		if seen[name] {
			return fail(fmt.Errorf("%s appears twice", name))
		}
		seen[name] = true
		if err := w.entry(); err != nil {
			return fail(err)
		}
		dst := stage + strings.TrimPrefix(name, target)
		mode := fs.FileMode(hdr.Mode & 0o755) //nolint:gosec // the mode was checked to have no bits above 0o777
		switch hdr.Typeflag {
		case tar.TypeDir:
			if dst != stage {
				if err := w.requireDirParent(dst, stage); err != nil {
					return fail(err)
				}
			}
			if err := w.data.Mkdir(dst, 0o700); err != nil {
				return fail(err)
			}
			dirs = append(dirs, dirMode{dst, mode | 0o700, hdr.ModTime})
		case tar.TypeReg:
			if dst == stage {
				// A single-file source.
				if err := w.writeEntry(tr, dst, hdr, mode); err != nil {
					return fail(err)
				}
				continue
			}
			if err := w.requireDirParent(dst, stage); err != nil {
				return fail(err)
			}
			if err := w.writeEntry(tr, dst, hdr, mode); err != nil {
				return fail(err)
			}
		default:
			return fail(fmt.Errorf("%s is a link, device or other special entry", name))
		}
	}
	if len(seen) == 0 {
		return fail(errors.New("the archive is empty"))
	}
	// Directory modes last, deepest first, so that a read-only directory
	// can still be filled.
	for _, d := range slices.Backward(dirs) {
		if err := w.data.Chmod(d.name, d.mode); err != nil {
			return fail(err)
		}
		_ = w.data.Chtimes(d.name, d.mod, d.mod)
	}
	if err := w.data.RemoveAll(target); err != nil {
		return fail(err)
	}
	if err := w.data.Rename(stage, target); err != nil {
		return err
	}
	w.logf("files %s restored: %d entries", target, len(seen))
	return nil
}

// checkEntryName accepts a clean relative name equal to target or below it.
func checkEntryName(name, target string) error {
	switch {
	case name == "" || path.IsAbs(name) || path.Clean(name) != name || slices.Contains(strings.Split(name, "/"), ".."):
		return fmt.Errorf("the name %q is not a clean relative name", hostsetup.Printable(name, 80))
	case !within(name, target):
		return fmt.Errorf("the name %q is outside %s", hostsetup.Printable(name, 80), target)
	}
	return checkName(name)
}

// requireDirParent checks that the parent of dst, up to stage, is a
// directory this extraction made: no earlier entry may have put anything
// else there.
func (w *worker) requireDirParent(dst, stage string) error {
	parent := path.Dir(dst)
	if parent == path.Dir(stage) && stage != dst {
		return fmt.Errorf("%s has no parent", dst)
	}
	info, err := w.data.Lstat(parent)
	if err != nil {
		return fmt.Errorf("the parent of %s is missing", strings.TrimPrefix(dst, stage))
	}
	if !info.IsDir() {
		return fmt.Errorf("the parent of %s is not a directory", strings.TrimPrefix(dst, stage))
	}
	return nil
}

func (w *worker) writeEntry(tr *tar.Reader, dst string, hdr *tar.Header, mode fs.FileMode) error {
	if hdr.Size < 0 {
		return errors.New("negative size")
	}
	f, err := w.data.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	n, err := io.Copy(limitedWriter{w: f, c: w}, io.LimitReader(tr, hdr.Size))
	if err == nil && n != hdr.Size {
		err = io.ErrUnexpectedEOF
	}
	if err == nil {
		err = f.Chmod(mode | 0o600)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		_ = w.data.Chtimes(dst, hdr.ModTime, hdr.ModTime)
	}
	return err
}

// describeSource names a source for logs and results.
func describeSource(s BackupSource) string {
	switch s.Type {
	case SourceFiles, SourceSQLite:
		return s.Type + " " + s.Path
	case SourcePostgres, SourceMongoDB:
		if s.All {
			return s.Type + " (all databases)"
		}
		return s.Type + " " + s.Database
	}
	return s.Type + " " + strings.Join(s.Argv, " ")
}

// artifactNames are the artifacts source i produces, in order. The helper
// computes them itself and reads exactly these.
func artifactNames(i int, s BackupSource) []string {
	base := fmt.Sprintf("%03d-%s", i, s.Type)
	switch s.Type {
	case SourceFiles:
		return []string{base + ".tar"}
	case SourceSQLite:
		return []string{base + ".db"}
	case SourcePostgres:
		return []string{base + ".dump"}
	case SourceMongoDB:
		if s.Users {
			return []string{base + ".archive", base + "-admin.archive", base + "-users.bson"}
		}
		return []string{base + ".archive"}
	}
	return []string{base + ".out"}
}
