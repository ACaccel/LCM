// Package upgrademarker is the file "lcm host upgrade" leaves in the data
// directory after it has validated an upgrade, and the server reads at its
// next start. It is the only statement of what the root command attests: the
// installed binary changed from one build to another. The server records a
// deployment of the self-deploy service from it, so the catalog agrees with
// the running binary without LCM claiming anything it did not observe.
//
// The package has no dependencies so that the root command and the server
// share the format without sharing code.
package upgrademarker

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// File is the marker's name in the data directory.
const File = "host-upgrade.json"

// MaxSize bounds what a reader accepts.
const MaxSize = 4 << 10

// Format is the only marker format there is.
const Format = 1

// Build identifies one binary as "lcm version" prints it.
type Build struct {
	Version string `json:"version"`
	// Commit is the abbreviated commit "lcm version" prints, 7 to 40
	// lowercase hexadecimal digits.
	Commit string `json:"commit"`
	Schema int    `json:"schema"`
}

// Marker says that the installed binary was replaced.
type Marker struct {
	Format int       `json:"format"`
	At     time.Time `json:"at"`
	From   Build     `json:"from"`
	To     Build     `json:"to"`
}

var (
	commitRe  = regexp.MustCompile(`^[0-9a-f]{7,40}$`)
	versionRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$`)
)

func (b Build) validate() error {
	switch {
	case !versionRe.MatchString(b.Version):
		return errors.New("malformed version")
	case !commitRe.MatchString(b.Commit):
		return errors.New("malformed commit")
	case b.Schema <= 0:
		return errors.New("malformed schema")
	}
	return nil
}

// Validate checks that the marker is well formed.
func (m Marker) Validate() error {
	if m.Format != Format {
		return fmt.Errorf("unsupported format %d", m.Format)
	}
	if m.At.IsZero() {
		return errors.New("no time")
	}
	if err := m.From.validate(); err != nil {
		return fmt.Errorf("from: %w", err)
	}
	if err := m.To.validate(); err != nil {
		return fmt.Errorf("to: %w", err)
	}
	return nil
}

// Encode returns the file content of a marker, whose format field is set.
func Encode(m Marker) ([]byte, error) {
	m.Format = Format
	m.At = m.At.UTC().Truncate(time.Second)
	if err := m.Validate(); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// Decode parses file content strictly: unknown fields, trailing data and
// malformed values are refused.
func Decode(data []byte) (Marker, error) {
	if len(data) > MaxSize {
		return Marker{}, errors.New("too large")
	}
	var m Marker
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return Marker{}, err
	}
	if dec.More() {
		return Marker{}, errors.New("trailing data")
	}
	if err := m.Validate(); err != nil {
		return Marker{}, err
	}
	return m, nil
}

// SameCommit reports whether two commits, either of which may be abbreviated
// (at least 7 digits), name the same commit.
func SameCommit(a, b string) bool {
	a, b = strings.ToLower(a), strings.ToLower(b)
	if len(a) < 7 || len(b) < 7 {
		return false
	}
	n := min(len(a), len(b))
	return a[:n] == b[:n]
}
