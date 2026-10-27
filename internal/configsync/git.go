package configsync

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ACaccel/lcm/internal/execx"
	"github.com/ACaccel/lcm/internal/model"
)

// Limits on what is read from a repository.
const (
	maxFiles     = 2000
	maxFileBytes = 8 << 20
	// maxListBytes bounds the output of ls-tree.
	maxListBytes = 32 << 20
	// localRef is the private ref the fetch writes the branch head to.
	localRef = "refs/lcm/head"
)

// gitEnv never prompts and allows only transports that cannot run commands
// (the ext:: transport can). execx passes only a short allowlist of the
// parent's variables, so the ssh command is passed on explicitly.
func gitEnv() []string {
	ssh := os.Getenv("GIT_SSH_COMMAND")
	if ssh == "" {
		ssh = "ssh -o BatchMode=yes"
	}
	return []string{
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ALLOW_PROTOCOL=file:git:http:https:ssh",
		"GIT_SSH_COMMAND=" + ssh,
		"LC_ALL=C",
	}
}

// git runs one git command in dir and returns its output.
func (s *Syncer) git(ctx context.Context, dir string, maxOut int, args ...string) (string, error) {
	if s.runner == nil {
		return "", errors.New("configsync: no command runner configured")
	}
	res, err := s.runner.Run(ctx, execx.Cmd{
		Argv: append([]string{s.gitBin}, args...), Dir: dir, Env: gitEnv(), Timeout: s.gitTime, MaxOutput: maxOut,
	})
	if err != nil {
		out := strings.TrimSpace(redact(res.Output))
		if len(out) > 300 {
			out = out[len(out)-300:]
		}
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, out)
	}
	if res.Truncated {
		return "", fmt.Errorf("git %s: output exceeds %d bytes", args[0], maxOut)
	}
	return res.Output, nil
}

// fetch updates LCM's own clone of the source and returns the head commit
// of its branch. The clone is a bare repository that is only ever written
// by fetches, so the user's working copy is never touched.
func (s *Syncer) fetch(ctx context.Context, src Source) (commit, dir string, err error) {
	dir, err = s.cloneDir(src.Name)
	if err != nil {
		return "", "", err
	}
	if _, statErr := os.Stat(filepath.Join(dir, "HEAD")); statErr != nil {
		if _, err := s.git(ctx, filepath.Dir(dir), 4<<10, "init", "--bare", "--quiet", "--", filepath.Base(dir)); err != nil {
			return "", "", err
		}
	}
	// The repository goes behind "--" so that it can never be read as an
	// option; the refspec forces the update because the branch may have
	// been rewritten upstream.
	if _, err := s.git(ctx, dir, 16<<10, "fetch", "--quiet", "--no-tags", "--force", "--",
		src.Repo, "+refs/heads/"+src.Branch+":"+localRef); err != nil {
		return "", "", err
	}
	out, err := s.git(ctx, dir, 4<<10, "rev-parse", "--verify", "--quiet", localRef+"^{commit}")
	if err != nil {
		return "", "", err
	}
	return strings.TrimSpace(out), dir, nil
}

// readDocuments lists the YAML files of the source's path at commit and
// splits them into documents. A file that cannot be read becomes a failed
// result instead of failing the whole sync.
func (s *Syncer) readDocuments(ctx context.Context, src Source, dir, commit string) ([]Document, []Result, error) {
	out, err := s.git(ctx, dir, maxListBytes, "ls-tree", "-r", "-z", commit)
	if err != nil {
		return nil, nil, err
	}
	var files []string
	for entry := range strings.SplitSeq(out, "\x00") {
		meta, name, ok := strings.Cut(entry, "\t")
		if !ok {
			continue
		}
		fields := strings.Fields(meta)
		// Symlinks (mode 120000) and submodules (type commit) are not
		// documents.
		if len(fields) != 3 || fields[1] != "blob" || fields[0] == "120000" {
			continue
		}
		if wanted(name, src.Path) {
			files = append(files, name)
		}
	}
	if len(files) > maxFiles {
		return nil, nil, fmt.Errorf("source has %d document files; the limit is %d", len(files), maxFiles)
	}

	var docs []Document
	var failures []Result
	for _, name := range files {
		content, err := s.git(ctx, dir, maxFileBytes, "cat-file", "blob", commit+":"+name)
		if err != nil {
			if ctx.Err() != nil {
				return nil, nil, ctx.Err()
			}
			failures = append(failures, Result{Path: name, Action: ActionFailed, Reason: "cannot read file", Issues: model.Issues{{Message: err.Error()}}})
			continue
		}
		docs = append(docs, SplitDocuments(name, []byte(content))...)
	}
	return docs, failures, nil
}

// wanted reports whether a repository file is a document file under path.
// Files named *.test.yaml belong to policy tests and are not documents.
func wanted(name, dir string) bool {
	lower := strings.ToLower(name)
	if !strings.HasSuffix(lower, ".yaml") && !strings.HasSuffix(lower, ".yml") {
		return false
	}
	if strings.HasSuffix(lower, ".test.yaml") || strings.HasSuffix(lower, ".test.yml") {
		return false
	}
	return dir == "." || name == dir || strings.HasPrefix(name, dir+"/")
}
