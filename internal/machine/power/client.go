package power

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/ACaccel/lcm/internal/execx"
)

// Client invokes the fixed root-owned wrapper using noninteractive sudo. The
// wrapper path is a deployment contract, never a Machine document property.
// Its privileged implementation must reject extra argv, clear env and execute only the
// root-owned, version-pinned helper entry point. Do not sudo the lcm binary.
type Client struct{ Runner execx.Runner }

const WrapperPath = "/usr/local/libexec/lcm-machine-power"

func (c Client) Call(ctx context.Context, req Request) (Response, error) {
	raw, err := json.Marshal(req)
	if err != nil {
		return Response{}, err
	}
	if _, err := DecodeRequest(bytes.NewReader(raw)); err != nil {
		return Response{}, err
	}
	if c.Runner == nil {
		return Response{}, errors.New("power client not configured")
	}
	result, runErr := c.Runner.Run(ctx, execx.Cmd{Argv: []string{"/usr/bin/sudo", "-n", "--", WrapperPath}, Stdin: raw, Timeout: 35 * time.Second, MaxOutput: MaxMessage})
	if result.Truncated {
		return Response{}, errors.New("truncated helper response; query receipt before retry")
	}
	var response Response
	if err := decodeStrict(bytes.NewBufferString(result.Output), MaxMessage, &response); err != nil {
		return Response{}, errors.New("invalid helper response; query receipt before retry")
	}
	if response.Version != Version || (response.Code == "") == (response.Receipt == nil) {
		return Response{}, errors.New("invalid helper response")
	}
	if response.Receipt != nil {
		r := response.Receipt
		if r.Binding != req.Binding || r.UpdatedAt <= 0 || (r.OSRequested && r.State != Accepted) {
			return Response{}, errors.New("helper receipt binding mismatch")
		}
		switch r.State {
		case Accepted, Submitting, Failed, Indeterminate:
		default:
			return Response{}, errors.New("invalid helper receipt state")
		}
	}
	if response.Code != "" {
		return response, errors.New(response.Code)
	}
	if runErr != nil {
		return response, runErr
	}
	return response, nil
}
