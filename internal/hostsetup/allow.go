package hostsetup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"reflect"
	"slices"
	"strings"
)

// AllowOptions control Allow.
type AllowOptions struct {
	// ConfigPath is the host configuration; empty selects ConfigPath.
	ConfigPath string
	// Name and Grant are the service entry to add or replace.
	Name  string
	Grant ServiceGrant
	// Check reports what would change without changing anything.
	Check bool
	// Out receives one line per step result.
	Out io.Writer
}

// Allow adds or replaces one entry of the services allowlist in the host
// configuration and converges that service's account and directories, the
// same steps apply runs for it. The configuration file is rewritten
// atomically, keeping the order and values of everything else; it is
// refused when the file changed after it was read. Runtimes and the deploy
// component stay with apply, which Allow names when they are missing.
func Allow(ctx context.Context, h Host, o AllowOptions) (Result, error) {
	if o.ConfigPath == "" {
		o.ConfigPath = ConfigPath
	}
	if o.Out == nil {
		o.Out = io.Discard
	}
	o.Out = safeWriter{w: o.Out}
	a := &applier{h: h, o: Options{ConfigPath: o.ConfigPath, Check: o.Check, Out: o.Out}, planned: map[string]bool{}}
	if err := a.allow(ctx, o.Name, o.Grant); err != nil {
		return a.res, safeError{err}
	}
	return a.res, nil
}

func (a *applier) allow(ctx context.Context, name string, g ServiceGrant) error {
	h, path := a.h, a.o.ConfigPath
	if !h.Privileged {
		return errors.New("must run as root, from a reviewed build: sudo bin/lcm host allow")
	}
	if err := CheckExecutable(h.Executable); err != nil {
		return err
	}
	if err := ValidServiceName(name); err != nil {
		return err
	}
	if err := trusted(h.FS, path); err != nil {
		return err
	}
	raw, info, err := readConfigFile(h.FS, path)
	if err != nil {
		return err
	}
	cfg, err := ParseConfig(raw)
	if err != nil {
		return fmt.Errorf("%s: %w; fix it first", path, err)
	}
	next := cfg
	next.Services = maps.Clone(cfg.Services)
	if next.Services == nil {
		next.Services = map[string]ServiceGrant{}
	}
	if len(g.Runtimes) == 0 {
		g.Runtimes = nil
	}
	if len(g.Inputs) == 0 {
		g.Inputs = nil
	}
	old, existed := cfg.Services[name]
	next.Services[name] = g
	if err := next.Validate(); err != nil {
		return err
	}
	if g.usesInstanceKeys() || next.Backup != nil && next.Backup.Tools != nil {
		// An older pinned helper parses host.json strictly and would refuse
		// every operation of every service once these keys exist.
		outdated, err := a.pinnedHelperOutdated()
		if err != nil {
			return err
		}
		if outdated {
			return fmt.Errorf("the pinned deploy helper %s is not this build, and an older helper cannot read instances, groups, after, wants or backup.tools; "+
				"run sudo bin/lcm host apply first, then this command again", DeployHelper)
		}
	}
	for _, t := range []string{"getent", "useradd"} {
		if _, err := h.Runner.LookPath(t); err != nil {
			return fmt.Errorf("missing required tool: %s", t)
		}
	}
	a.cfg = next
	if err := a.preflightAccounts(ctx); err != nil {
		return err
	}
	a.uidMin = uidMin(h.FS)
	if !g.External {
		st, err := a.checkGrant(ctx, name, g, a.uidMin)
		if err != nil {
			return err
		}
		if st.problem != "" {
			return fmt.Errorf("cannot allow %s: %s", name, st.problem)
		}
		a.grants = map[string]grantState{name: st}
	}
	data, err := setService(raw, name, g)
	if err != nil {
		return err
	}
	if again, err := ParseConfig(data); err != nil || !reflect.DeepEqual(again, next) {
		return fmt.Errorf("the rewritten %s would not hold the intended configuration; nothing was changed", path)
	}

	s := &step{a: a, name: "host.json"}
	if !existed || !reflect.DeepEqual(old, g) {
		what := "add services." + name + " to " + path
		if existed {
			what = "update services." + name + " in " + path
		}
		if err := s.change(what, func() error {
			now, nowInfo, err := readConfigFile(h.FS, path)
			if err != nil {
				return err
			}
			if !bytes.Equal(now, raw) || !sameFile(nowInfo, info) {
				return fmt.Errorf("%s changed since it was read; nothing was changed, run the command again", path)
			}
			return h.FS.WriteFile(path, bytes.NewReader(data), info.Mode.Perm(), 0, 0)
		}); err != nil {
			return fmt.Errorf("host.json: %w", err)
		}
	}
	s.finish()

	gs := &step{a: a, name: "service " + name}
	if err := a.grant(ctx, gs, name); err != nil {
		return fmt.Errorf("service %s: %w", name, err)
	}
	gs.finish()

	var later []string
	if !next.Components.Deploy && !g.External {
		later = append(later, "components.deploy is false, so the deploy helper is not installed")
	}
	if g.Route != nil && next.Components.Ingress == nil {
		later = append(later, "components.ingress is null, so the route is not published")
	} else if g.Route != nil {
		later = append(later, "the route is not published yet")
	}
	for _, r := range g.Runtimes {
		if _, err := h.FS.Lstat(RuntimesDir + "/" + r); err != nil {
			later = append(later, "the runtime "+r+" is not copied yet")
		}
	}
	if len(later) > 0 {
		_, _ = fmt.Fprintf(a.o.Out, "\n%s; complete it with: sudo bin/lcm host apply\n", strings.Join(later, "; "))
	}
	return nil
}

// pinnedHelperOutdated reports whether a pinned deploy helper is installed
// that is not the running build.
func (a *applier) pinnedHelperOutdated() (bool, error) {
	if _, err := a.h.FS.Lstat(DeployHelper); errors.Is(err, fs.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	pinned, err := hashFile(a.h.FS, DeployHelper)
	if err != nil {
		return false, err
	}
	if a.h.OpenExecutable == nil {
		return false, errors.New("the running executable cannot be read")
	}
	exe, err := a.h.OpenExecutable()
	if err != nil {
		return false, fmt.Errorf("read the running executable: %w", err)
	}
	defer func() { _ = exe.Close() }()
	sum := sha256.New()
	if _, err := io.Copy(sum, exe); err != nil {
		return false, fmt.Errorf("read the running executable: %w", err)
	}
	return hex.EncodeToString(sum.Sum(nil)) != pinned, nil
}

// readConfigFile reads the configuration file and the description of the
// file it read.
func readConfigFile(fsys FS, path string) ([]byte, Info, error) {
	info, err := fsys.Lstat(path)
	if err != nil {
		return nil, Info{}, err
	}
	f, err := fsys.Open(path)
	if err != nil {
		return nil, Info{}, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxConfigSize+1))
	if err != nil {
		return nil, Info{}, err
	}
	if len(data) > maxConfigSize {
		return nil, Info{}, fmt.Errorf("%s is larger than %d bytes", path, maxConfigSize)
	}
	return data, info, nil
}

// member is one key and value of a JSON object, in file order.
type member struct {
	key   string
	value json.RawMessage
}

func objectMembers(data []byte) ([]member, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, errors.New("not a JSON object")
	}
	var out []member
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := tok.(string)
		if !ok {
			return nil, errors.New("not a JSON object")
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		out = append(out, member{key: key, value: v})
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	return out, nil
}

func encodeMembers(ms []member) (json.RawMessage, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, m := range ms {
		if i > 0 {
			b.WriteByte(',')
		}
		key, err := json.Marshal(m.key)
		if err != nil {
			return nil, err
		}
		b.Write(key)
		b.WriteByte(':')
		if err := json.Compact(&b, m.value); err != nil {
			return nil, err
		}
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// setService returns the configuration file data with services.<name> set
// to g: an existing entry keeps its place, a new one comes last, and every
// other key keeps its order and value. The result is indented like Encode.
func setService(data []byte, name string, g ServiceGrant) ([]byte, error) {
	top, err := objectMembers(data)
	if err != nil {
		return nil, err
	}
	entry, err := json.Marshal(g)
	if err != nil {
		return nil, err
	}
	at := slices.IndexFunc(top, func(m member) bool { return m.key == "services" })
	var services []member
	if at >= 0 && string(bytes.TrimSpace(top[at].value)) != "null" {
		if services, err = objectMembers(top[at].value); err != nil {
			return nil, err
		}
	}
	if i := slices.IndexFunc(services, func(m member) bool { return m.key == name }); i >= 0 {
		services[i].value = entry
	} else {
		services = append(services, member{key: name, value: entry})
	}
	value, err := encodeMembers(services)
	if err != nil {
		return nil, err
	}
	if at >= 0 {
		top[at].value = value
	} else {
		top = append(top, member{key: "services", value: value})
	}
	compact, err := encodeMembers(top)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := json.Indent(&out, compact, "", "  "); err != nil {
		return nil, err
	}
	out.WriteByte('\n')
	return out.Bytes(), nil
}
