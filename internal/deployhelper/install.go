package deployhelper

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"

	"github.com/ACaccel/lcm/internal/hostsetup"
)

// releasePerm is the mode of an installed file or directory: directories
// 0755, files 0644, or 0755 when the built file had an execute bit.
func releasePerm(info hostsetup.Info) fs.FileMode {
	if info.IsDir() || info.Mode&0o111 != 0 {
		return 0o755
	}
	return 0o644
}

// install copies the artifact directory of a successful build into a new
// root-owned release. The build tree belongs to the service's build
// account, whose build ran third-party code, so the copy is confined to it,
// accepts only entries that account owns and refuses links that leave the
// release.
func (h *Helper) install(ctx context.Context, req Request) Response {
	resp, svc := reply(req), req.Service
	dir := serviceBuilds(svc) + "/" + req.BuildID
	var rec buildRecord
	if err := h.readJSON(dir+"/build.json", &rec); errors.Is(err, fs.ErrNotExist) {
		return refuse(resp, CodeNotFound, "there is no build %s of %s", req.BuildID, svc)
	} else if err != nil {
		return refuse(resp, CodeFailed, "read the build record: %v", err)
	}
	switch {
	case rec.Service != svc || rec.BuildID != req.BuildID:
		return refuse(resp, CodeFailed, "the record of build %s is not that build's", req.BuildID)
	case rec.State != StateSucceeded:
		return refuse(resp, CodeRefused, "build %s did not succeed", req.BuildID)
	}
	builder, err := h.builder(ctx, svc)
	if err != nil {
		return refuse(resp, CodeRefused, "%v", err)
	}
	releases := hostsetup.ServiceReleases(svc)
	if _, err := h.trustedPath(releases, false); err != nil {
		return refuse(resp, CodeRefused, "%v; run sudo bin/lcm host allow or host apply", err)
	}
	// Installs that are never activated must not fill the disk: keep at most
	// as many releases as activate may keep, and current and previous.
	opt := hostsetup.ServiceOptDir(svc)
	current, err := h.linkTarget(opt + "/current")
	if err == nil {
		var previous string
		if previous, err = h.linkTarget(opt + "/previous"); err == nil {
			h.pruneReleases(svc, MaxKeepReleases-1, current, previous)
		}
	}
	if err != nil {
		return refuse(resp, CodeFailed, "%v", err)
	}
	id := h.newID()
	dst := releases + "/" + id
	err = hostsetup.CopyTree(h.FS, dir+"/src", req.Artifact, dst, hostsetup.CopyOptions{
		Owners: map[int]bool{builder.UID: true}, UID: h.RootUID, GID: h.RootGID, Perm: releasePerm,
	})
	if errors.Is(err, fs.ErrNotExist) {
		return refuse(resp, CodeNotFound, "the artifact %s is not in build %s: %v", req.Artifact, req.BuildID, err)
	}
	if err != nil {
		return refuse(resp, CodeFailed, "copy the artifact: %v", err)
	}
	sum, size, files, err := treeDigest(h.FS, dst)
	m := Manifest{
		Version: Version, Service: svc, ReleaseID: id, BuildID: req.BuildID, Commit: rec.Commit,
		Runtimes: rec.Runtimes, Artifact: req.Artifact, TreeSHA256: sum, Size: size, Files: files,
		CreatedAt: h.Clock.Now().UnixMilli(),
	}
	if err == nil {
		err = h.writeJSON(manifestPath(svc, id), m)
	}
	if err != nil {
		_ = h.FS.RemoveAll(dst)
		return refuse(resp, CodeFailed, "record the release: %v", err)
	}
	resp.Install = &InstallResult{ReleaseID: id, Manifest: m}
	return resp
}

func manifestPath(svc, id string) string { return hostsetup.ServiceReleases(svc) + "/" + id + ".json" }

// treeDigest is the SHA-256 over every entry of a root-owned tree, in path
// order: its type, path, mode and size, and the SHA-256 of a file's content
// or a link's target. It also returns the total size and number of files.
func treeDigest(fsys hostsetup.FS, root string) (string, int64, int, error) {
	digest := sha256.New()
	var size int64
	var files int
	var walk func(rel string) error
	walk = func(rel string) error {
		names, err := fsys.ReadDir(path.Join(root, rel))
		if err != nil {
			return err
		}
		for _, n := range names {
			r := path.Join(rel, n)
			p := path.Join(root, r)
			info, err := fsys.Lstat(p)
			if err != nil {
				return err
			}
			entry := []any{"", r, fmt.Sprintf("%04o", info.Mode.Perm()), info.Size, ""}
			switch {
			case info.IsDir():
				entry[0], entry[3] = "d", 0
			case info.IsLink():
				target, err := fsys.Readlink(p)
				if err != nil {
					return err
				}
				entry[0], entry[3], entry[4] = "l", 0, target
			case info.IsRegular():
				sum, err := fileDigest(fsys, p)
				if err != nil {
					return err
				}
				entry[0], entry[4] = "f", sum
				size += info.Size
				files++
			default:
				return fmt.Errorf("%s is not a file, directory or symbolic link", p)
			}
			line, _ := json.Marshal(entry) // strings and integers always marshal
			digest.Write(append(line, '\n'))
			if info.IsDir() {
				if err := walk(r); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk("."); err != nil {
		return "", 0, 0, err
	}
	return hex.EncodeToString(digest.Sum(nil)), size, files, nil
}

func fileDigest(fsys hostsetup.FS, p string) (string, error) {
	f, err := fsys.Open(p)
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
