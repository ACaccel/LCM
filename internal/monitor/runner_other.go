//go:build !unix

package monitor

import "os/exec"

func setProcessGroup(*exec.Cmd) {}
