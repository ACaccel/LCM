package hostsetup

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"
)

// ConfigPath is the default host configuration file.
const ConfigPath = "/etc/lcm/host.json"

// DefaultRepository is the repository LCM deploys itself from unless the
// configuration names another.
const DefaultRepository = "https://github.com/ACaccel/LCM.git"

// maxConfigSize bounds the configuration file.
const maxConfigSize = 64 << 10

// Config is the host configuration, /etc/lcm/host.json. It holds no secrets
// and nothing that names the host: the tailnet name comes from Tailscale.
type Config struct {
	// Operator is the administrator account that owns the toolchains to
	// copy and receives the first API token.
	Operator   string     `json:"operator"`
	Repository string     `json:"repository"`
	Toolchains Toolchains `json:"toolchains"`
	Tailscale  Tailscale  `json:"tailscale"`
	Components Components `json:"components"`
	// Runtimes are the named execution environments that managed services
	// may use, by name, with the directory host setup copies to
	// /opt/lcm/runtimes/<name>.
	Runtimes map[string]string `json:"runtimes,omitempty"`
	Backup   *Backup           `json:"backup,omitempty"`
	// Services is the allowlist of the deploy helper: only services named
	// here can be built, installed and run, with what their grant says.
	Services map[string]ServiceGrant `json:"services,omitempty"`
}

// Backup configures where service backups are kept.
type Backup struct {
	Dir string `json:"dir"`
}

// ServiceGrant is what one managed service may use. Documents name a
// service but can never widen its grant.
type ServiceGrant struct {
	// Port is where the service listens on 127.0.0.1; an external grant
	// with a static route needs none.
	Port     int      `json:"port,omitempty"`
	Runtimes []string `json:"runtimes,omitempty"`
	// Inputs are read-only directories outside the repository that a build
	// may read, by name.
	Inputs map[string]string `json:"inputs,omitempty"`
	// External marks a service whose process and accounts LCM does not
	// own: host setup creates nothing for it and the deploy helper refuses
	// its deploy and backup operations; only its route is used.
	External bool `json:"external,omitempty"`
	// Route publishes the service through the ingress component.
	Route *RouteGrant `json:"route,omitempty"`
}

// RouteGrant is the part of a route only the host configuration decides.
type RouteGrant struct {
	// Prefix is the path below the domain, "/" or ending with "/".
	Prefix string `json:"prefix"`
	// Static is a root-owned directory served at the prefix.
	Static string `json:"static,omitempty"`
}

// Toolchains are the Go and Node.js installations that self-deploy builds
// with; host setup copies them under /opt/lcm/toolchain.
type Toolchains struct {
	Go   string `json:"go"`
	Node string `json:"node"`
}

// Tailscale configures the tailnet-only HTTPS proxy to LCM.
type Tailscale struct {
	Port int `json:"port"`
}

// Components selects the optional host components. Turning one off never
// removes it.
type Components struct {
	MachinePower bool          `json:"machinePower"`
	SecurityHost *SecurityHost `json:"securityHost"`
	// Deploy installs the deploy helper, its sudoers rule and the build
	// account.
	Deploy bool `json:"deploy"`
	// Ingress renders and applies LCM's nginx site for the services'
	// routes; nil leaves nginx alone.
	Ingress *Ingress `json:"ingress,omitempty"`
}

// SecurityHost enables the restricted security host helper.
type SecurityHost struct {
	// MachineID is the immutable ID of the registered Machine resource.
	MachineID string `json:"machineId"`
}

var (
	accountName = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
	httpsURL    = regexp.MustCompile(`^https://[A-Za-z0-9./_-]+$`)
	machineID   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,99}$`)
)

// Validate checks every field and reports all problems at once.
func (c Config) Validate() error {
	var errs []error
	if !accountName.MatchString(c.Operator) || c.Operator == "root" {
		errs = append(errs, fmt.Errorf("operator must name a non-root account (got %q)", c.Operator))
	}
	if !httpsURL.MatchString(c.Repository) || strings.Contains(c.Repository, "..") {
		errs = append(errs, fmt.Errorf("repository must be an https URL without credentials (got %q)", c.Repository))
	}
	for _, tc := range []struct{ name, dir string }{{"toolchains.go", c.Toolchains.Go}, {"toolchains.node", c.Toolchains.Node}} {
		if !filepath.IsAbs(tc.dir) || filepath.Clean(tc.dir) != tc.dir || tc.dir == "/" || strings.ContainsAny(tc.dir, "\n\x00") {
			errs = append(errs, fmt.Errorf("%s must be an absolute, clean directory path (got %q)", tc.name, tc.dir))
		}
	}
	if c.Tailscale.Port < 1 || c.Tailscale.Port > 65535 {
		errs = append(errs, fmt.Errorf("tailscale.port must be between 1 and 65535 (got %d)", c.Tailscale.Port))
	}
	if sh := c.Components.SecurityHost; sh != nil && !machineID.MatchString(sh.MachineID) {
		errs = append(errs, fmt.Errorf("components.securityHost.machineId must be the Machine's immutable ID (got %q)", sh.MachineID))
	}
	errs = append(errs, c.validateDeploy()...)
	errs = append(errs, c.validateIngress()...)
	return errors.Join(errs...)
}

// Encode returns the configuration as the indented JSON file content.
func (c Config) Encode() []byte {
	b, _ := json.MarshalIndent(c, "", "  ") // plain structs always marshal
	return append(b, '\n')
}

// keys is the expected object layout of the configuration: each key maps to
// the layout of its object value, or nil for a scalar.
type keys map[string]keys

var configKeys = keys{
	"operator":   nil,
	"repository": nil,
	"toolchains": {"go": nil, "node": nil},
	"tailscale":  {"port": nil},
	"components": {"machinePower": nil, "securityHost": {"machineId": nil}, "deploy": nil, "ingress": {
		"domain": nil, "listen": nil, "acmeWebroot": nil, "tls": {"email": nil, "keyType": nil}, "hsts": nil,
		"wellKnown": {anyKey: nil}, "adopt": nil, "legacyLock": nil,
	}},
	"runtimes": {anyKey: nil},
	"backup":   {"dir": nil},
	"services": {anyKey: {
		"port": nil, "runtimes": nil, "inputs": {anyKey: nil}, "external": nil,
		"route": {"prefix": nil, "static": nil}, "extraPaths": reservedKey,
	}},
}

// anyKey in a layout accepts every key of a map-like object; its names are
// validated with the values.
const anyKey = "*"

// reservedKey marks a key that a later version will accept, so that it is
// refused with a message that says so rather than as a mistyped key.
var reservedKey = keys{reservedMark: nil}

// reservedMark is not a valid JSON object key of any layout.
const reservedMark = "\x00reserved"

func isReserved(k keys) bool {
	_, ok := k[reservedMark]
	return ok
}

// ParseConfig decodes and validates a configuration. Decoding is strict:
// unknown, duplicate or differently cased keys and trailing data are
// refused, because a mistyped key would otherwise be ignored silently.
func ParseConfig(data []byte) (Config, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := checkKeys(dec, configKeys, ""); err != nil {
		return Config{}, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return Config{}, errors.New("unexpected data after the configuration object")
	}
	var c Config
	strict := json.NewDecoder(bytes.NewReader(data))
	strict.DisallowUnknownFields()
	if err := strict.Decode(&c); err != nil {
		return Config{}, err
	}
	return c, c.Validate()
}

// checkKeys reads one JSON object (or null below the top level) from dec and
// verifies its keys against want.
func checkKeys(dec *json.Decoder, want keys, at string) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if tok == nil && at != "" {
		return nil
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		if at == "" {
			return errors.New("the configuration must be a JSON object")
		}
		return fmt.Errorf("%s must be an object", at)
	}
	seen := map[string]bool{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		key, _ := tok.(string)
		name := strings.TrimPrefix(at+"."+key, ".")
		sub, known := want[key]
		if !known {
			sub, known = want[anyKey]
		}
		switch {
		case !known:
			return fmt.Errorf("unknown key %q", name)
		case isReserved(sub):
			return fmt.Errorf("%q is reserved for a later version; remove it", name)
		case seen[key]:
			return fmt.Errorf("duplicate key %q", name)
		}
		seen[key] = true
		if sub != nil {
			if err := checkKeys(dec, sub, name); err != nil {
				return err
			}
			continue
		}
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return err
		}
	}
	_, err = dec.Token()
	return err
}

// LoadConfig reads the configuration file. It must be a regular file owned
// by root, not writable by group or others, in root-owned directories that
// are not symbolic links and not writable by group or others.
func LoadConfig(fsys FS, path string) (Config, error) {
	if err := trusted(fsys, path); err != nil {
		return Config{}, err
	}
	f, err := fsys.Open(path)
	if err != nil {
		return Config{}, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxConfigSize+1))
	if err != nil {
		return Config{}, err
	}
	if len(data) > maxConfigSize {
		return Config{}, fmt.Errorf("%s is larger than %d bytes", path, maxConfigSize)
	}
	c, err := ParseConfig(data)
	if err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// trusted checks that path and each of its ancestors exist, are owned by
// root, are not symbolic links and are not writable by group or others.
func trusted(fsys FS, path string) error {
	for _, p := range ancestry(path) {
		info, err := fsys.Lstat(p)
		if errors.Is(err, fs.ErrNotExist) && p == path {
			return fmt.Errorf("%s does not exist", path)
		}
		if err != nil {
			return err
		}
		if err := rootOwned(p, info); err != nil {
			return err
		}
	}
	return nil
}

// rootOwned refuses files that someone other than root could change.
func rootOwned(path string, info Info) error {
	switch {
	case info.IsLink():
		return fmt.Errorf("%s is a symbolic link; host files and their directories must not be", path)
	case info.UID != 0:
		return fmt.Errorf("%s is not owned by root", path)
	case info.Mode.Perm()&0o022 != 0:
		return fmt.Errorf("%s is writable by group or others", path)
	}
	return nil
}

// ancestry returns "/", each ancestor of path and path itself, in order.
func ancestry(path string) []string {
	var out []string
	for p := path; ; p = filepath.Dir(p) {
		out = append(out, p)
		if p == "/" {
			break
		}
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}
