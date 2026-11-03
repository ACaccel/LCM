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

// systemRunner passes a bounded JSON message to a fixed privileged entry point.
// No helper output or stderr is returned as an error; it may contain local data.
type systemRunner struct{}

func (systemRunner) Run(ctx context.Context, request Request) (Response, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	input, err := json.Marshal(request)
	if err != nil || len(input) > 16384 {
		return Response{}, ErrUnavailable
	}
	cmd := exec.CommandContext(ctx, "/usr/bin/sudo", "-n", "/usr/bin/python3", "-I", "/usr/local/libexec/lcm/security-host/helper.py")
	cmd.Dir = "/"
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C.UTF-8"}
	cmd.Stdin = bytes.NewReader(input)
	var output boundedBuffer
	cmd.Stdout = &output
	cmd.Stderr = io.Discard
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
	if len(p)+b.Len() > 65536 {
		return 0, errors.New("securityhost: oversized helper response")
	}
	return b.Buffer.Write(p)
}
