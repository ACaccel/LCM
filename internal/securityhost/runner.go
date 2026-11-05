package securityhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"time"
)

// helperCommand is the fixed privileged entry point of the security helper.
var helperCommand = []string{"/usr/bin/sudo", "-n", "/usr/bin/python3", "-I", "/usr/local/libexec/lcm/security-host/helper.py"}

// systemRunner returns the runner of the installed helper.
func systemRunner() commandRunner {
	return commandRunner{argv: helperCommand, waitDelay: helperWaitDelay}
}

// commandRunner passes a bounded JSON message to a fixed command. No helper
// output or stderr is returned as an error; it may contain local data.
type commandRunner struct {
	argv []string
	// waitDelay bounds the wait for the output after the command was
	// killed; zero selects helperWaitDelay.
	waitDelay time.Duration
}

const (
	// helperTimeout exceeds the helper's own work and rollback budgets
	// (WORK_SECONDS + ROLLBACK_SECONDS in helper.py), so a request normally
	// ends with the helper's recorded outcome rather than with LCM giving up.
	helperTimeout = 45 * time.Second
	// helperWaitDelay bounds how long a timed-out call waits for the
	// helper's output to close. The timeout kills only sudo; the helper
	// runs as root, out of LCM's reach, and may hold stdout open until it
	// finishes. The call then fails and the operation's outcome is
	// recovered by a later query.
	helperWaitDelay = 5 * time.Second
	// maxRequestBytes and maxResponseBytes bound the messages exchanged
	// with the helper.
	maxRequestBytes  = 16384
	maxResponseBytes = 65536
)

func (r commandRunner) Run(ctx context.Context, request Request) (Response, error) {
	ctx, cancel := context.WithTimeout(ctx, helperTimeout)
	defer cancel()
	input, err := json.Marshal(request)
	if err != nil || len(input) > maxRequestBytes {
		return Response{}, ErrUnavailable
	}
	cmd := exec.CommandContext(ctx, r.argv[0], r.argv[1:]...) //nolint:gosec // argv is fixed at construction, never from a request
	cmd.Dir = "/"
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C.UTF-8"}
	cmd.Stdin = bytes.NewReader(input)
	var output boundedBuffer
	cmd.Stdout = &output
	cmd.Stderr = io.Discard
	cmd.WaitDelay = r.waitDelay
	if cmd.WaitDelay <= 0 {
		cmd.WaitDelay = helperWaitDelay
	}
	if err = cmd.Run(); err != nil {
		return Response{}, ErrUnavailable
	}
	var response Response
	decoder := json.NewDecoder(bytes.NewReader(output.Bytes()))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&response); err != nil || response.Version != ProtocolVersion {
		return Response{}, ErrUnavailable
	}
	if err = decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return Response{}, ErrUnavailable
	}
	switch response.Error {
	case "":
		return response, nil
	case "conflict":
		return Response{}, ErrConflict
	case "not-found":
		return Response{}, ErrNotFound
	default:
		return Response{}, ErrUnavailable
	}
}

type boundedBuffer struct{ bytes.Buffer }

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if len(p)+b.Len() > maxResponseBytes {
		return 0, errors.New("securityhost: oversized helper response")
	}
	return b.Buffer.Write(p)
}
