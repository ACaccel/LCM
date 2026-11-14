package main

import (
	"context"
	"fmt"
	"io"

	"github.com/ACaccel/lcm/internal/client"
)

const policyUsage = `Usage: lcm policy <pause|resume> <name> [flags]

Commands:
  pause <name>   Stop the policy's triggers from starting runs; active runs continue
  resume <name>  Let the policy's triggers start runs again
`

func (a *app) policy(ctx context.Context, args []string) error {
	fs, conn := a.clientFlags("policy")
	fs.Usage = func() { _, _ = io.WriteString(a.stderr, policyUsage); fs.PrintDefaults() }
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 || (pos[0] != "pause" && pos[0] != "resume") {
		return usagef("policy: expected pause|resume <name>")
	}
	cl, err := a.newClient(conn)
	if err != nil {
		return err
	}
	if err := cl.JSON(ctx, "POST", client.Path("policies", pos[1], pos[0]), nil, nil, nil); err != nil {
		return apiError(err)
	}
	_, err = fmt.Fprintf(a.stdout, "%s %s: request accepted\n", pos[0], pos[1])
	return err
}
