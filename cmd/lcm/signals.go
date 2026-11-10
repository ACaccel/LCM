package main

import (
	"os"
	"os/signal"
	"syscall"
)

// shutdownSignals are the signals that cancel a command's context. The host
// commands that change the machine (apply, upgrade) also take SIGHUP, which
// an SSH drop sends through sudo: without it they would die in the middle
// of a change, and an upgrade would leave LCM stopped or running with
// automation off instead of rolling back.
func shutdownSignals(args []string) []os.Signal {
	sigs := []os.Signal{syscall.SIGINT, syscall.SIGTERM}
	if changesHost(args) {
		sigs = append(sigs, syscall.SIGHUP)
	}
	return sigs
}

// changesHost reports whether args name a host command that changes the
// machine.
func changesHost(args []string) bool {
	if len(args) < 2 || args[0] != "host" {
		return false
	}
	switch args[1] {
	case "apply", "upgrade", "cert", "ingress":
		return true
	}
	return false
}

// protectHostChange keeps a vanished terminal from ending the process
// through SIGPIPE when it writes its progress; the write just fails.
func protectHostChange(args []string) {
	if changesHost(args) {
		signal.Ignore(syscall.SIGPIPE)
	}
}
