// Package connection loads the host-configured connections through which
// LCM reaches resources that are not local, such as a router's web
// interface.
//
// Connections are host configuration: a file only the host administrator
// can change (LCM_CONNECTIONS_FILE), never a document, never synced from Git
// and never writable through the API. A resource document can only name a
// connection; what the connection allows (actions, resources) is decided
// here and cannot be widened by a document. Passwords are referenced by
// secret name and read through internal/secret; they are never part of a
// Connection value, its errors or its API form.
package connection

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/ACaccel/lcm/internal/router/xiaomi"
	"github.com/ACaccel/lcm/internal/secret"
)

// Connection types.
const (
	TypeXiaomi = "xiaomi"
)

// Actions a connection can allow. Writes not listed in a connection's
// AllowActions are refused.
const (
	ActionReboot = "reboot"
)

// typeActions lists the actions each connection type can allow.
var typeActions = map[string][]string{
	TypeXiaomi: {ActionReboot},
}

// maxFileSize bounds the connections file.
const maxFileSize = 1 << 20

var nameRe = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,61}[a-z0-9])?$`)

// Connection is one validated connection. It holds the name of the password
// secret, never the password.
type Connection struct {
	Name               string
	Type               string
	URL                string
	Username           string
	PasswordSecret     string
	DeviceID           string
	InsecureSkipVerify bool
	// AllowActions lists the write actions resources may perform through
	// the connection.
	AllowActions []string
	// AllowResources lists the resource names that may use the
	// connection; empty allows every resource.
	AllowResources []string
}

// Allows reports whether the connection permits action.
func (c Connection) Allows(action string) bool { return slices.Contains(c.AllowActions, action) }

// AllowsResource reports whether the resource named name may use the
// connection.
func (c Connection) AllowsResource(name string) bool {
	return len(c.AllowResources) == 0 || slices.Contains(c.AllowResources, name)
}

// Set is the immutable set of configured connections. The zero value and a
// nil *Set are empty.
type Set struct {
	items []Connection
}

// Get returns the connection named name.
func (s *Set) Get(name string) (Connection, bool) {
	if s == nil {
		return Connection{}, false
	}
	for _, c := range s.items {
		if c.Name == name {
			return c, true
		}
	}
	return Connection{}, false
}

// List returns the connections in file order.
func (s *Set) List() []Connection {
	if s == nil {
		return nil
	}
	out := make([]Connection, len(s.items))
	for i, c := range s.items {
		c.AllowActions = slices.Clone(c.AllowActions)
		c.AllowResources = slices.Clone(c.AllowResources)
		out[i] = c
	}
	return out
}

// Check explains why the resource named resource cannot use the connection
// named name for a resource reached through adapter (which names the
// connection type it needs); it returns "" when it can. The message is
// suitable for a validation issue at spec.connection.
func (s *Set) Check(name, adapter, resource string) string {
	c, ok := s.Get(name)
	switch {
	case !ok:
		return fmt.Sprintf("connection %q is not configured on this host", name)
	case c.Type != adapter:
		return fmt.Sprintf("connection %q is of type %s, the adapter needs %s", name, c.Type, adapter)
	case !c.AllowsResource(resource):
		return fmt.Sprintf("connection %q does not allow resource %q", name, resource)
	}
	return ""
}

// file is the YAML form of the connections file.
type file struct {
	Connections []entry `yaml:"connections"`
}

type entry struct {
	Name               string   `yaml:"name"`
	Type               string   `yaml:"type"`
	URL                string   `yaml:"url"`
	Username           string   `yaml:"username"`
	PasswordSecret     string   `yaml:"passwordSecret"`
	DeviceID           string   `yaml:"deviceId"`
	InsecureSkipVerify bool     `yaml:"insecureSkipVerify"`
	AllowActions       []string `yaml:"allowActions"`
	AllowResources     []string `yaml:"allowResources"`
}

// Load reads and validates the connections file at path. An empty path is
// an empty set. The file must be a regular file that neither group nor
// others can write, and every password secret must resolve through
// secrets now, so that a misconfiguration stops the server at startup.
func Load(path string, secrets secret.Source) (*Set, error) {
	if path == "" {
		return &Set{}, nil
	}
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("connections file: %w", err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("connections file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("connections file %s: not a regular file", path)
	}
	if info.Mode().Perm()&0o022 != 0 {
		return nil, fmt.Errorf("connections file %s: must not be writable by group or others (mode %04o)", path, info.Mode().Perm())
	}
	data, err := io.ReadAll(io.LimitReader(f, maxFileSize+1))
	if err != nil {
		return nil, fmt.Errorf("connections file: %w", err)
	}
	if len(data) > maxFileSize {
		return nil, fmt.Errorf("connections file %s: larger than %d bytes", path, maxFileSize)
	}
	s, err := Parse(data, secrets)
	if err != nil {
		return nil, fmt.Errorf("connections file %s: %w", path, err)
	}
	return s, nil
}

// Parse decodes and validates connections file content strictly: unknown
// fields, duplicate names, unknown types or actions, URLs with credentials
// or a path, and unresolvable secrets are errors. Errors name fields and
// connections, never secret values.
func Parse(data []byte, secrets secret.Source) (*Set, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var f file
	if err := dec.Decode(&f); err != nil {
		if errors.Is(err, io.EOF) {
			return &Set{}, nil
		}
		return nil, fmt.Errorf("invalid YAML: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("must contain a single YAML document")
	}
	var errs []error
	seen := map[string]bool{}
	out := &Set{}
	for i, e := range f.Connections {
		at := fmt.Sprintf("connections[%d]", i)
		if e.Name != "" {
			at = fmt.Sprintf("connection %q", e.Name)
		}
		c, err := validate(e, secrets)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", at, err))
			continue
		}
		if seen[c.Name] {
			errs = append(errs, fmt.Errorf("%s: duplicate name", at))
			continue
		}
		seen[c.Name] = true
		out.items = append(out.items, c)
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return out, nil
}

func validate(e entry, secrets secret.Source) (Connection, error) {
	c := Connection{
		Name: e.Name, Type: e.Type, URL: strings.TrimSpace(e.URL), Username: e.Username,
		PasswordSecret: e.PasswordSecret, DeviceID: e.DeviceID, InsecureSkipVerify: e.InsecureSkipVerify,
		AllowActions: slices.Clone(e.AllowActions), AllowResources: slices.Clone(e.AllowResources),
	}
	if !nameRe.MatchString(c.Name) {
		return c, errors.New("name must be 1-63 lowercase letters, digits or '-', starting with a letter")
	}
	actions, known := typeActions[c.Type]
	if !known {
		return c, fmt.Errorf("unknown type %q (known: %s)", c.Type, TypeXiaomi)
	}
	if err := validateURL(c.URL); err != nil {
		return c, err
	}
	if c.Username == "" {
		c.Username = xiaomi.DefaultUsername
	}
	if !secret.ValidName(c.PasswordSecret) {
		return c, errors.New("passwordSecret must name a secret (letters, digits, '.', '_' or '-')")
	}
	seen := map[string]bool{}
	for _, a := range c.AllowActions {
		switch {
		case !slices.Contains(actions, a):
			return c, fmt.Errorf("allowActions: unknown action %q for type %s", a, c.Type)
		case seen[a]:
			return c, fmt.Errorf("allowActions: duplicate action %q", a)
		}
		seen[a] = true
	}
	seen = map[string]bool{}
	for _, r := range c.AllowResources {
		switch {
		case !nameRe.MatchString(r):
			return c, fmt.Errorf("allowResources: %q is not a resource name", r)
		case seen[r]:
			return c, fmt.Errorf("allowResources: duplicate resource %q", r)
		}
		seen[r] = true
	}
	pw, err := secrets.Get(c.PasswordSecret)
	if err != nil {
		return c, fmt.Errorf("passwordSecret %q cannot be resolved", c.PasswordSecret)
	}
	if pw.Reveal() == "" {
		return c, fmt.Errorf("passwordSecret %q is empty", c.PasswordSecret)
	}
	// The type's client validates the rest (device ID, username) without
	// network I/O.
	if _, err := xiaomi.New(xiaomi.Config{BaseURL: c.URL, Username: c.Username, Password: pw.Reveal(), DeviceID: c.DeviceID, InsecureSkipVerify: c.InsecureSkipVerify}); err != nil {
		return c, err
	}
	return c, nil
}

// validateURL accepts an http or https URL of a host, without credentials,
// path beyond "/", query or fragment.
func validateURL(raw string) error {
	u, err := url.Parse(raw)
	switch {
	case raw == "":
		return errors.New("url is required")
	case err != nil:
		return errors.New("url is not a valid URL")
	case u.Scheme != "http" && u.Scheme != "https":
		return errors.New("url must use http or https")
	case u.User != nil:
		return errors.New("url must not contain credentials; use passwordSecret")
	case u.Hostname() == "":
		return errors.New("url must name a host")
	case u.Path != "" && u.Path != "/":
		return errors.New("url must not have a path")
	case u.RawQuery != "" || u.Fragment != "" || u.Opaque != "":
		return errors.New("url must not have a query or fragment")
	}
	return nil
}
