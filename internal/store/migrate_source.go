package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/ACaccel/lcm/internal/execx"
	"github.com/ACaccel/lcm/internal/model"
)

const sourceMigration = 15

// gitConfigTimeout bounds each read of a checkout's git configuration.
const gitConfigTimeout = 10 * time.Second

var remoteNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// legacySource is a service source before schema 15: the checkout LCM
// deployed from and the name of its remote.
type legacySource struct{ repo, remote string }

// migrateServiceSources is the Go part of 0015_service_source.sql. Every
// stored version of every service document, and the repository documents
// config sync keeps for services, replaces source.repo and source.remote
// with source.url, the URL of that remote in the checkout's git config.
//
// LCM now keeps the build checkout of a service at <work dir>/<name>, so the
// current document of a managed or retired service must already use that
// checkout. A version whose checkout no longer exists takes the URL of the
// resource's current version. Any problem refuses the whole migration: the
// transaction is not committed, the schema stays at 14 and the previous
// binary keeps working, and the error says what to fix.
func migrateServiceSources(ctx context.Context, tx *sql.Tx, env migrateEnv) error {
	type service struct {
		id, name, phase string
		version         int
	}
	var services []service
	err := queryRows(ctx, tx, `SELECT id, name, phase, version FROM resources WHERE type = 'service' ORDER BY name, id`, func(rs *sql.Rows) error {
		var s service
		if err := rs.Scan(&s.id, &s.name, &s.phase, &s.version); err != nil {
			return err
		}
		services = append(services, s)
		return nil
	})
	if err != nil {
		return err
	}

	var problems []error
	byName := map[string]*sourceResolver{}
	for _, s := range services {
		r := &sourceResolver{env: env, name: s.name, managed: s.phase != "unmanaged", urls: map[legacySource]string{}}
		if r.managed || byName[s.name] == nil {
			byName[s.name] = r
		}
		if err := rewriteVersions(ctx, tx, s.id, s.version, r); err != nil {
			problems = append(problems, err)
		}
	}
	if err := rewriteOwnership(ctx, tx, env, byName); err != nil {
		problems = append(problems, err)
	}
	if len(problems) > 0 {
		return fmt.Errorf("service sources must name a repository URL now (source.url); nothing was changed and the "+
			"database stays at schema 14, so the previous lcm binary still runs on it. Fix the following, then start this version again:\n%w",
			errors.Join(problems...))
	}
	return nil
}

// rewriteVersions rewrites the versions of one service resource, the
// current one first so that older versions can fall back to its URL.
func rewriteVersions(ctx context.Context, tx *sql.Tx, id string, current int, r *sourceResolver) error {
	type version struct {
		n   int
		doc string
	}
	var versions []version
	err := queryRows(ctx, tx, `SELECT version, document FROM resource_versions WHERE resource_id = ?
		ORDER BY version = ? DESC, version DESC`, func(rs *sql.Rows) error {
		var v version
		if err := rs.Scan(&v.n, &v.doc); err != nil {
			return err
		}
		versions = append(versions, v)
		return nil
	}, id, current)
	if err != nil {
		return err
	}
	var problems []error
	for _, v := range versions {
		what := fmt.Sprintf("service %s version %d", r.name, v.n)
		doc, changed, err := r.rewrite(ctx, []byte(v.doc), v.n == current, what)
		if err != nil {
			problems = append(problems, err)
			continue
		}
		if !changed {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE resource_versions SET document = ? WHERE resource_id = ? AND version = ?`, string(doc), id, v.n); err != nil {
			return err
		}
	}
	return errors.Join(problems...)
}

// rewriteOwnership rewrites the repository documents config sync recorded
// for services, so that they keep matching the stored versions.
func rewriteOwnership(ctx context.Context, tx *sql.Tx, env migrateEnv, byName map[string]*sourceResolver) error {
	type row struct{ name, doc, pending string }
	var rows []row
	err := queryRows(ctx, tx, `SELECT name, document, pending FROM config_ownership WHERE kind = 'Resource' ORDER BY name`, func(rs *sql.Rows) error {
		var r row
		if err := rs.Scan(&r.name, &r.doc, &r.pending); err != nil {
			return err
		}
		rows = append(rows, r)
		return nil
	})
	if err != nil {
		return err
	}
	var problems []error
	for _, o := range rows {
		r := byName[o.name]
		if r == nil {
			r = &sourceResolver{env: env, name: o.name, urls: map[legacySource]string{}}
			byName[o.name] = r
		}
		doc, changed, err := r.rewrite(ctx, []byte(o.doc), false, "repository document of "+o.name)
		if err != nil {
			problems = append(problems, err)
			continue
		}
		pending, pendingChanged := []byte(o.pending), false
		if o.pending != "" {
			if pending, pendingChanged, err = r.rewrite(ctx, pending, false, "pending repository document of "+o.name); err != nil {
				problems = append(problems, err)
				continue
			}
		}
		if !changed && !pendingChanged {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE config_ownership SET document = ?, pending = ? WHERE kind = 'Resource' AND name = ?`,
			string(doc), string(pending), o.name); err != nil {
			return err
		}
	}
	return errors.Join(problems...)
}

// sourceResolver finds the URLs of one resource's legacy sources. Each
// checkout and remote is read once.
type sourceResolver struct {
	env  migrateEnv
	name string
	// managed is true for a managed or retired resource, whose current
	// version is deployed from its build checkout after the upgrade.
	managed bool
	urls    map[legacySource]string
	// fallback is the URL of the current version, or the first one read,
	// for versions whose checkout is gone.
	fallback string
}

// rewrite returns doc with source.url in place of a legacy source, and
// whether it changed. current marks the resource's current version.
func (r *sourceResolver) rewrite(ctx context.Context, doc []byte, current bool, what string) ([]byte, bool, error) {
	src, ok := legacySourceOf(doc)
	if !ok {
		return doc, false, nil
	}
	url, err := r.resolve(ctx, src, current)
	if err != nil {
		return nil, false, fmt.Errorf("%s: %w", what, err)
	}
	out, err := withSourceURL(doc, url, r.env.workDir)
	if err != nil {
		return nil, false, fmt.Errorf("%s: source URL %s read from %s: %w", what, execx.RedactURLs(url), src.repo, err)
	}
	return out, true, nil
}

func (r *sourceResolver) resolve(ctx context.Context, src legacySource, current bool) (string, error) {
	if u, ok := r.urls[src]; ok {
		return u, nil
	}
	checkout := filepath.Join(r.env.workDir, r.name)
	strict := current && r.managed
	if strict && filepath.Clean(src.repo) != checkout {
		return "", fmt.Errorf("source.repo is %s, but LCM now keeps the build checkout of %s at %s and clones it there itself. "+
			"With the previous binary, either move the checkout (mv %s %s) and save the service with source.repo %s, or remove source from the service",
			src.repo, r.name, checkout, src.repo, checkout, checkout)
	}
	if !strict {
		if _, err := os.Stat(src.repo); errors.Is(err, fs.ErrNotExist) {
			if r.fallback != "" {
				return r.fallback, nil
			}
			return "", fmt.Errorf("the checkout %s no longer exists and no other version of %s names a readable one; "+
				"recreate it (git clone <url> %s, or git init %s && git -C %s remote add %s <url>) so that its URL can be read",
				src.repo, r.name, src.repo, src.repo, src.repo, src.remote)
		}
	}
	u, err := r.read(ctx, src)
	if err != nil {
		return "", err
	}
	r.urls[src] = u
	if r.fallback == "" {
		r.fallback = u
	}
	return u, nil
}

// read returns remote.<remote>.url from the checkout's own git config file.
// Reading the file directly keeps git from finding another repository in a
// parent directory or applying the user's configuration.
func (r *sourceResolver) read(ctx context.Context, src legacySource) (string, error) {
	if !remoteNameRe.MatchString(src.remote) {
		return "", fmt.Errorf("source.remote %q is not a remote name", src.remote)
	}
	key := "remote." + src.remote + ".url"
	file := filepath.Join(src.repo, ".git", "config")
	if _, err := os.Stat(file); err != nil {
		return "", fmt.Errorf("cannot read %s: the checkout %s is missing or not a clone (%v); clone it again "+
			"(git clone <url> %s) or remove source from the service with the previous binary", key, src.repo, errors.Unwrap(err), src.repo)
	}
	res, err := r.env.runner.Run(ctx, execx.Cmd{
		Argv:    []string{r.env.git, "config", "--file", file, "--get", key},
		Env:     execx.GitEnv(),
		Timeout: gitConfigTimeout,
	})
	u := strings.TrimSpace(res.Output)
	if err != nil || u == "" {
		// git config exits 1 for a key that is not set.
		reason := "it is not set"
		if err != nil && execx.ExitCode(err) != 1 {
			reason = execx.RedactURLs(err.Error())
		}
		return "", fmt.Errorf("cannot read %s of the checkout %s: %s; add the remote (git -C %s remote add %s <url>) "+
			"or remove source from the service with the previous binary", key, src.repo, reason, src.repo, src.remote)
	}
	if execx.CheckGitRemote(u) != nil {
		return "", fmt.Errorf("%s of %s carries credentials, which LCM would store and show: %s. "+
			"Set it without them (git -C %s remote set-url %s <url>) and use an SSH key or a git credential helper",
			key, src.repo, execx.RedactURLs(u), src.repo, src.remote)
	}
	if p, ok := model.LocalRepoPath(u); ok && model.PathWithin(p, r.env.workDir) {
		return "", fmt.Errorf("%s of %s is %s, inside LCM's build directory %s; point it at the repository new versions come from", key, src.repo, u, r.env.workDir)
	}
	return u, nil
}

// legacySourceOf returns the repo and remote of a stored service document
// that still uses them.
func legacySourceOf(doc []byte) (legacySource, bool) {
	var d struct {
		Spec struct {
			Type   string `json:"type"`
			Config struct {
				Source *struct {
					Repo   string `json:"repo"`
					Remote string `json:"remote"`
				} `json:"source"`
			} `json:"config"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(doc, &d); err != nil || d.Spec.Type != model.ResourceService || d.Spec.Config.Source == nil {
		return legacySource{}, false
	}
	s := d.Spec.Config.Source
	if s.Repo == "" && s.Remote == "" {
		return legacySource{}, false
	}
	remote := s.Remote
	if remote == "" {
		remote = "origin"
	}
	return legacySource{repo: s.Repo, remote: remote}, true
}

// withSourceURL replaces the legacy source fields of doc with url and
// returns the canonical encoding, validated like any document LCM writes.
func withSourceURL(doc []byte, url, workDir string) ([]byte, error) {
	var d map[string]any
	if err := decodeJSON(doc, &d); err != nil {
		return nil, err
	}
	spec, _ := d["spec"].(map[string]any)
	cfg, _ := spec["config"].(map[string]any)
	src, _ := cfg["source"].(map[string]any)
	if src == nil {
		return nil, errors.New("no source in the document")
	}
	delete(src, "repo")
	delete(src, "remote")
	src["url"] = url
	b, err := json.Marshal(d)
	if err != nil {
		return nil, err
	}
	r, err := model.DecodeResource(b)
	if err != nil {
		return nil, err
	}
	if p, ok := model.LocalRepoPath(url); ok && model.PathWithin(p, workDir) {
		return nil, fmt.Errorf("it is inside LCM's build directory %s", workDir)
	}
	return model.Encode(r)
}

// convertServiceV1 converts a stored lcm/v1 Service to lcm/v2 for migration
// 14 with its source unchanged. The model's converter refuses a v1 source,
// whose URL only the checkout knows; migration 15 rewrites it.
func convertServiceV1(data []byte) ([]byte, []string, error) {
	var doc map[string]any
	if err := decodeJSON(data, &doc); err != nil {
		return model.ConvertServiceV1JSON(data)
	}
	spec, _ := doc["spec"].(map[string]any)
	src, has := spec["source"]
	if !has {
		return model.ConvertServiceV1JSON(data)
	}
	delete(spec, "source")
	b, err := json.Marshal(doc)
	if err != nil {
		return nil, nil, err
	}
	out, warns, err := model.ConvertServiceV1JSON(b)
	if err != nil {
		return nil, warns, err
	}
	var v2 map[string]any
	if err := decodeJSON(out, &v2); err != nil {
		return nil, warns, err
	}
	v2spec, _ := v2["spec"].(map[string]any)
	cfg, _ := v2spec["config"].(map[string]any)
	if cfg == nil {
		cfg = map[string]any{}
		v2spec["config"] = cfg
	}
	cfg["source"] = src
	out, err = json.Marshal(v2)
	return out, warns, err
}

// decodeJSON decodes keeping numbers as written.
func decodeJSON(b []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	return dec.Decode(v)
}
