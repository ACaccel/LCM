package execx

import (
	"errors"
	"os"
	"regexp"
	"strings"
)

// GitAllowProtocol lists the git transports LCM permits. Remote helpers
// such as ext:: can run arbitrary commands and are excluded.
const GitAllowProtocol = "file:git:http:https:ssh"

// GitEnv returns the environment entries for every git command LCM runs:
// git never prompts (an unattended run would hang), uses only transports
// that cannot run commands, and keeps messages unlocalized because callers
// parse them. Runner adds these to its short allowlist of parent variables,
// so LCM's own secrets never reach git; an operator's GIT_SSH_COMMAND is the
// one parent value passed on, because ssh remotes may need it.
func GitEnv() []string {
	ssh := os.Getenv("GIT_SSH_COMMAND")
	if ssh == "" {
		ssh = "ssh -o BatchMode=yes"
	}
	return []string{
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ALLOW_PROTOCOL=" + GitAllowProtocol,
		"GIT_SSH_COMMAND=" + ssh,
		"LC_ALL=C",
	}
}

// ErrGitCredentials reports a repository URL that embeds credentials.
var ErrGitCredentials = errors.New("must not embed credentials; use an SSH key or a git credential helper")

// CheckGitRemote refuses repository URLs whose userinfo could be a secret.
// LCM stores, displays and logs repository URLs, so a token in one would
// leak. A URL with a scheme may carry only the conventional ssh user "git";
// scp-like addresses (user@host:path) and local paths are accepted because
// ssh never takes a password from them.
func CheckGitRemote(repo string) error {
	scheme, rest, ok := strings.Cut(repo, "://")
	if !ok {
		return nil
	}
	authority, _, _ := strings.Cut(rest, "/")
	at := strings.LastIndex(authority, "@")
	if at < 0 {
		return nil
	}
	switch strings.ToLower(scheme) {
	case "ssh", "git+ssh", "ssh+git":
		if authority[:at] == "git" {
			return nil
		}
	}
	return ErrGitCredentials
}

var urlUserinfo = regexp.MustCompile(`(://)[^/@\s]+@`)

// RedactURLs removes the userinfo of every URL quoted in s, so that command
// output or errors mentioning a repository never carry its credentials.
func RedactURLs(s string) string { return urlUserinfo.ReplaceAllString(s, "$1") }
