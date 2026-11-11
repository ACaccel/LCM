package hostsetup

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestExcerptKeepsTheTailOnOneLine(t *testing.T) {
	in := "l1\nl2\n\nnginx: [emerg] unknown directive \"x\" in /etc/nginx/a.conf:3\r\nnginx: configuration file /etc/nginx/nginx.conf test failed\n\x1b[2J"
	got := Excerpt(in)
	if strings.ContainsAny(got, "\n\r\x1b") {
		t.Fatalf("excerpt is not one printable line: %q", got)
	}
	want := `l1 | l2 | nginx: [emerg] unknown directive "x" in /etc/nginx/a.conf:3 | nginx: configuration file /etc/nginx/nginx.conf test failed | ?[2J`
	if got != want {
		t.Errorf("excerpt = %q, want %q", got, want)
	}
	var many []string
	for i := range 20 {
		many = append(many, "line"+string(rune('a'+i)))
	}
	got = Excerpt(strings.Join(many, "\n"))
	if strings.Contains(got, "linea") || !strings.HasSuffix(got, "linet") || strings.Count(got, "|") != excerptLines-1 {
		t.Errorf("excerpt does not keep the last %d lines: %q", excerptLines, got)
	}
	if got := Excerpt(strings.Repeat("x", 5000)); len(got) > excerptBytes+3 {
		t.Errorf("excerpt is %d bytes", len(got))
	}
	if Excerpt(" \n\t\n") != "" {
		t.Error("blank output has an excerpt")
	}
}

func TestPrintableRendersLineBreaks(t *testing.T) {
	if got := printable("a\r\nb\n\n c\td", 100); got != "a | b | c d" {
		t.Errorf("printable = %q", got)
	}
}

func TestOSRunnerFailureCarriesStderrTail(t *testing.T) {
	script := "i=0; while [ $i -lt 2000 ]; do echo noise-$i >&2; i=$((i+1)); done; echo 'E: cause' >&2; printf 'W: summary\\033[2J\\n' >&2; exit 3"
	_, err := OSRunner{}.Run(context.Background(), Command{Path: "/bin/sh", Args: []string{"-c", script}})
	var ee *ExitError
	if !errors.As(err, &ee) || ee.Code != 3 {
		t.Fatalf("err = %v", err)
	}
	if !strings.HasSuffix(ee.Stderr, "E: cause | W: summary?[2J") || strings.ContainsAny(err.Error(), "\n\x1b") {
		t.Errorf("stderr excerpt = %q", ee.Stderr)
	}
	if !strings.Contains(err.Error(), "exited with status 3: ") {
		t.Errorf("message = %q", err)
	}
}

func TestCommandMessageNamesTheCause(t *testing.T) {
	err := errors.New("exit status 1")
	got := commandMessage("nginx: [emerg] bad\nnginx: test failed\n", err)
	if got != "nginx: [emerg] bad | nginx: test failed (exit status 1)" {
		t.Errorf("message = %q", got)
	}
	if got := commandMessage("", err); got != "exit status 1" {
		t.Errorf("message = %q", got)
	}
}
