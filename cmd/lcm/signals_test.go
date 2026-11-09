package main

import (
	"syscall"
	"testing"
)

func TestShutdownSignals(t *testing.T) {
	has := func(args []string, want syscall.Signal) bool {
		for _, s := range shutdownSignals(args) {
			if s == want {
				return true
			}
		}
		return false
	}
	for _, args := range [][]string{{"host", "upgrade"}, {"host", "upgrade", "--check"}, {"host", "apply"}} {
		if !has(args, syscall.SIGHUP) {
			t.Errorf("%v must handle SIGHUP, which an SSH drop sends", args)
		}
	}
	for _, args := range [][]string{{"serve"}, {"host", "status"}, {"host", "init"}, {"host"}, nil} {
		if has(args, syscall.SIGHUP) {
			t.Errorf("%v must keep the default SIGHUP behavior", args)
		}
	}
	for _, args := range [][]string{{"host", "upgrade"}, {"serve"}} {
		if !has(args, syscall.SIGINT) || !has(args, syscall.SIGTERM) {
			t.Errorf("%v must handle SIGINT and SIGTERM", args)
		}
	}
}
