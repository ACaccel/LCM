package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/ACaccel/lcm/internal/client"
	"github.com/ACaccel/lcm/internal/configsync"
)

// Environment variables of the API client commands.
const (
	envServer    = "LCM_SERVER"
	envToken     = "LCM_TOKEN"      //nolint:gosec // names an environment variable, not a credential
	envTokenFile = "LCM_TOKEN_FILE" //nolint:gosec // names an environment variable, not a credential

	defaultServer = "http://127.0.0.1:7800"
)

// problemError reports that a command ran but found problems in what it was
// given (invalid or skipped documents). It exits with status 3.
type problemError struct{ msg string }

func (e problemError) Error() string { return e.msg }

// connection holds the flags that locate and authenticate a server.
type connection struct {
	server    string
	tokenFile string
}

// clientFlags creates the flag set of an API client command.
func (a *app) clientFlags(name string) (*flag.FlagSet, *connection) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	c := &connection{}
	fs.StringVar(&c.server, "server", "", "server URL (default $"+envServer+" or "+defaultServer+")")
	// The token itself is deliberately not a flag: it would show up in the
	// process list and shell history.
	fs.StringVar(&c.tokenFile, "token-file", "", "file holding the API token (default $"+envTokenFile+"; the token can also be in $"+envToken+")")
	return fs, c
}

// parseArgs parses flags that may be mixed with positional arguments, as in
// "lcm run <policy> --wait", and returns the positional ones. Flag errors
// are usage errors.
func parseArgs(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil, err
			}
			return nil, usagef("%s: %v", fs.Name(), err)
		}
		if fs.NArg() == 0 {
			return pos, nil
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

// newClient builds the API client from the flags and the environment.
func (a *app) newClient(c *connection) (*client.Client, error) {
	server := firstNonEmpty(c.server, a.getenv(envServer), defaultServer)
	token, err := a.loadToken(c.tokenFile)
	if err != nil {
		return nil, err
	}
	cl, err := client.New(server, token, nil)
	if err != nil {
		return nil, usagef("%v", err)
	}
	return cl, nil
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

// loadToken finds the API token: a token file given by flag, then
// $LCM_TOKEN, then $LCM_TOKEN_FILE.
func (a *app) loadToken(flagFile string) (string, error) {
	file := flagFile
	if file == "" {
		if t := strings.TrimSpace(a.getenv(envToken)); t != "" {
			return t, nil
		}
		file = a.getenv(envTokenFile)
	}
	if file == "" {
		return "", usagef("no API token: set %s or %s, or pass --token-file (create one with: lcm token create --name <name>)", envToken, envTokenFile)
	}
	info, err := os.Stat(file)
	if err != nil {
		return "", fmt.Errorf("token file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("token file %s is not a regular file", file)
	}
	if info.Mode().Perm()&0o007 != 0 {
		return "", fmt.Errorf("token file %s can be read by other users (mode %04o); run: chmod 600 %s", file, info.Mode().Perm(), file)
	}
	raw, err := os.ReadFile(file) //nolint:gosec // the path is the operator's own token file
	if err != nil {
		return "", fmt.Errorf("token file: %w", err)
	}
	for line := range strings.SplitSeq(string(raw), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line, nil
		}
	}
	return "", fmt.Errorf("token file %s is empty", file)
}

// multiFlag collects a repeatable string flag.
type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

// docFile is one document with the file it came from.
type docFile = configsync.Document

// readDocuments reads the documents of files and directories ("-" is
// standard input). Directories are searched recursively for *.yaml and
// *.yml files, except *.test.yaml, in name order.
func (a *app) readDocuments(paths []string) ([]docFile, error) {
	var docs []docFile
	add := func(name string, data []byte) {
		docs = append(docs, configsync.SplitDocuments(name, data)...)
	}
	for _, p := range paths {
		if p == "-" {
			data, err := io.ReadAll(a.stdin)
			if err != nil {
				return nil, fmt.Errorf("read standard input: %w", err)
			}
			add("-", data)
			continue
		}
		info, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			data, err := os.ReadFile(p) //nolint:gosec // the path is an operator argument
			if err != nil {
				return nil, err
			}
			add(p, data)
			continue
		}
		var files []string
		err = filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() && isDocumentFile(d.Name()) {
				files = append(files, path)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		sort.Strings(files)
		for _, f := range files {
			data, err := os.ReadFile(f) //nolint:gosec // found by walking an operator-given directory
			if err != nil {
				return nil, err
			}
			add(f, data)
		}
	}
	if len(docs) == 0 {
		return nil, fmt.Errorf("no documents found in %s", strings.Join(paths, ", "))
	}
	return docs, nil
}

func isDocumentFile(name string) bool {
	n := strings.ToLower(name)
	if strings.HasSuffix(n, ".test.yaml") || strings.HasSuffix(n, ".test.yml") {
		return false
	}
	return strings.HasSuffix(n, ".yaml") || strings.HasSuffix(n, ".yml")
}

// joinDocuments builds the request body: the documents separated by
// document markers, so the server splits them back into the same list.
func joinDocuments(docs []docFile) []byte {
	var buf bytes.Buffer
	for i, d := range docs {
		if i > 0 {
			buf.WriteString("\n---\n")
		}
		buf.Write(d.Data)
	}
	return buf.Bytes()
}

// table writes aligned columns without colour.
type table struct {
	tw *tabwriter.Writer
}

func newTable(w io.Writer, header ...string) *table {
	t := &table{tw: tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)}
	t.row(header...)
	return t
}

func (t *table) row(cells ...string) {
	_, _ = fmt.Fprintln(t.tw, strings.Join(cells, "\t"))
}

func (t *table) flush() error { return t.tw.Flush() }

// apiError turns a server error into the message the user sees. Validation
// problems are listed one per line.
func apiError(err error) error {
	var ae *client.Error
	if !errors.As(err, &ae) {
		return err
	}
	issues := ae.Issues()
	if len(issues) == 0 {
		return err
	}
	var sb strings.Builder
	sb.WriteString(ae.Error())
	for _, is := range issues {
		sb.WriteString("\n  ")
		if is.Path != "" {
			sb.WriteString(is.Path + ": ")
		}
		sb.WriteString(is.Message)
	}
	return errors.New(sb.String())
}

// lines splits text into lines without the trailing empty one.
func lines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}
