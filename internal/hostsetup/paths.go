package hostsetup

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/ACaccel/lcm/deploy"
)

// Host paths. They match deploy/ (unit, sudoers rules, self-install) and the
// documented layout in docs/operations.md.
const (
	DataDir         = "/var/lib/lcm"
	WorkDir         = DataDir + "/work"
	CacheDir        = DataDir + "/cache"
	Checkout        = WorkDir + "/lcm"
	DatabaseFile    = DataDir + "/lcm.db"
	EtcDir          = "/etc/lcm"
	EnvFile         = EtcDir + "/lcm.env"
	OptDir          = "/opt/lcm"
	ToolchainDir    = OptDir + "/toolchain"
	LibexecDir      = "/usr/local/libexec/lcm"
	Binary          = "/usr/local/bin/lcm"
	SelfInstall     = LibexecDir + "/self-install"
	Sudoers         = "/etc/sudoers.d/lcm"
	Unit            = "/etc/systemd/system/lcm.service"
	UnitName        = "lcm.service"
	HealthURL       = "http://127.0.0.1:7800/api/v1/health"
	ServeTarget     = "http://127.0.0.1:7800"
	ServiceAccount  = "lcm"
	PowerWrapper    = "/usr/local/libexec/lcm-machine-power"
	PowerHelper     = "/usr/local/libexec/lcm-machine-helper"
	PowerSudoers    = "/etc/sudoers.d/lcm-machine-power"
	PowerConfig     = "/etc/lcm/machine-power.json"
	PowerLedger     = "/var/lib/lcm-machine-power"
	SecurityHelper  = LibexecDir + "/security-host/helper.py"
	SecurityPolicy  = "/etc/lcm/security-host/policy.json"
	SecuritySudoers = "/etc/sudoers.d/lcm-security-host"
	// The security host installer runs these by absolute path.
	python3 = "/usr/bin/python3"
	visudo  = "/usr/sbin/visudo"
)

// Component names, as status and apply report them.
const (
	ComponentCore         = "core"
	ComponentMachinePower = "machine-power"
	ComponentSecurityHost = "security-host"
)

// rootFile is a file installed as root with fixed content, owner and mode.
type rootFile struct {
	path string
	perm fs.FileMode
	// asset names the embedded deploy file; empty means the running lcm
	// executable.
	asset string
}

var (
	coreFiles = []rootFile{
		{path: SelfInstall, perm: 0o755, asset: "self-install"},
		{path: Sudoers, perm: 0o440, asset: "sudoers"},
		{path: Unit, perm: 0o644, asset: "lcm.service"},
	}
	powerFiles = []rootFile{
		{path: PowerWrapper, perm: 0o755, asset: "machine-power"},
		{path: PowerHelper, perm: 0o755},
		{path: PowerSudoers, perm: 0o440, asset: "machine-power-sudoers"},
	}
	securityFiles = []rootFile{
		{path: SecurityHelper, perm: 0o644, asset: "security-host/helper.py"},
	}
)

// asset returns an embedded deploy file.
func asset(name string) []byte {
	b, err := fs.ReadFile(deploy.Files, name)
	if err != nil {
		panic("hostsetup: missing embedded deploy file " + name) // a build without deploy/ files cannot install anything
	}
	return b
}

// Content is file content identified by its SHA-256.
type Content struct {
	Sum  string // hex SHA-256
	open func() (io.ReadCloser, error)
}

func bytesContent(b []byte) Content {
	sum := sha256.Sum256(b)
	return Content{Sum: hex.EncodeToString(sum[:]), open: func() (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(string(b))), nil
	}}
}

// FileContent hashes a regular file of fsys; the content is read again when
// it is installed.
func FileContent(fsys FS, path string) (Content, error) {
	sum, err := hashFile(fsys, path)
	if err != nil {
		return Content{}, err
	}
	return Content{Sum: sum, open: func() (io.ReadCloser, error) { return fsys.Open(path) }}, nil
}

func hashFile(fsys FS, path string) (string, error) {
	f, err := fsys.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// SelfDeployed reports whether exe is the binary LCM's self-deploy builds
// and installs, or one of its copies. Whoever can push to the deployed
// branch controls that binary, so it must never run as root.
func SelfDeployed(exe string) bool {
	exe = filepath.Clean(exe)
	dir, base := filepath.Split(exe)
	if filepath.Clean(dir) == filepath.Dir(Binary) && (base == "lcm" || strings.HasPrefix(base, "lcm.")) {
		return true
	}
	return strings.HasPrefix(exe, DataDir+"/")
}
