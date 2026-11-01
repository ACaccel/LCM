package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/ACaccel/lcm/internal/client"
)

const machineUsage = `Usage: lcm machine <command> <name> [flags]

Commands:
  observe                     Collect a read-only observation
  history | events            Read a page (--limit, --cursor)
  capabilities                Show available actions and their prerequisites
  maintenance <name> [preview|enter|exit]  Read, enter or exit maintenance
  operations                  List operations (--limit, --cursor)
  operation <name> <id>        Read a durable operation
  reboot | shutdown           Submit a power request; acceptance is not completion
  resolve <name> <id>          Close an operation with an unverified outcome
  retire | resume | unmanage  Change management phase without changing OS power

All writes require --version, --reason, --confirm <name> and --acknowledge.
Acknowledgement covers partial workload coverage, controller downtime and manual
recovery. Power requests also require your own --idempotency-key; reuse that same
key and version after an ambiguous response. Never submit a new key to retry.
No command cancels an OS power request. Resolve does not exit maintenance.
The current API records reasons for maintenance and resolve only; power and
management phase reasons are checked locally.
`

var machineKeyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)

type machineFlags struct {
	version     int
	reason      string
	confirm     string
	acknowledge bool
	key         string
	limit       int
	cursor      string
	format      string
	previewHash string
}

func (a *app) machine(ctx context.Context, args []string) error {
	fs, conn := a.clientFlags("machine")
	fs.Usage = func() { _, _ = io.WriteString(a.stderr, machineUsage); fs.PrintDefaults() }
	var f machineFlags
	fs.IntVar(&f.version, "version", 0, "current document version (If-Match; required for writes)")
	fs.StringVar(&f.reason, "reason", "", "reason for a management, maintenance or power action")
	fs.StringVar(&f.confirm, "confirm", "", "confirm the exact target machine name")
	fs.BoolVar(&f.acknowledge, "acknowledge", false, "acknowledge partial workload coverage, controller downtime and manual recovery")
	fs.StringVar(&f.key, "idempotency-key", "", "caller-chosen power request key; reuse on ambiguous retry")
	fs.IntVar(&f.limit, "limit", 100, "page size (1-199)")
	fs.StringVar(&f.cursor, "cursor", "", "opaque cursor from the previous page")
	fs.StringVar(&f.format, "o", "json", "output format: json or yaml")
	fs.StringVar(&f.previewHash, "preview-hash", "", "reviewed maintenance preview hash (required for enter)")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	req, power, err := machineRequest(pos, f)
	if err != nil {
		return err
	}
	cl, err := a.newClient(conn)
	if err != nil {
		return err
	}
	resp, err := cl.Do(ctx, req)
	if err != nil {
		if power {
			return fmt.Errorf("%w; if submission is ambiguous, inspect operations and retry only with the same --idempotency-key and --version", apiError(err))
		}
		return apiError(err)
	}
	operationResponse := power || pos[0] == "operation" || pos[0] == "resolve"
	if len(resp.Body) == 0 {
		if operationResponse {
			return fmt.Errorf("server returned no operation ID or state; inspect operations before retrying with the same key and version")
		}
		emptyAllowed := pos[0] == "unmanage" || pos[0] == "maintenance" && len(pos) == 3 && pos[2] == "exit"
		if !emptyAllowed {
			return fmt.Errorf("server returned an empty response")
		}
		_, err = fmt.Fprintf(a.stdout, "%s %s: request accepted\n", pos[0], pos[1])
		return err
	}
	var value any
	if err := json.Unmarshal(resp.Body, &value); err != nil {
		if power {
			return fmt.Errorf("decode response: %w; inspect operations before retrying with the same --idempotency-key and --version", err)
		}
		return fmt.Errorf("decode response: %w", err)
	}
	operationState := ""
	if operationResponse {
		op, ok := value.(map[string]any)
		id, _ := op["id"].(string)
		state, _ := op["state"].(string)
		if !ok || strings.TrimSpace(id) == "" || strings.TrimSpace(state) == "" {
			return fmt.Errorf("server returned an operation without ID or state; inspect operations before retrying with the same key and version")
		}
		operationState = state
	}
	if f.format == "yaml" {
		enc := yaml.NewEncoder(a.stdout)
		enc.SetIndent(2)
		if err := enc.Encode(value); err != nil {
			return err
		}
		if err := enc.Close(); err != nil {
			return err
		}
	} else {
		enc := json.NewEncoder(a.stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(value); err != nil {
			return err
		}
	}
	if power && (operationState == "failed" || operationState == "cancelled") {
		return fmt.Errorf("power operation is %s; inspect the operation before taking further action", operationState)
	}
	return nil
}

func machineRequest(pos []string, f machineFlags) (client.Request, bool, error) {
	fail := func(msg string) (client.Request, bool, error) {
		return client.Request{}, false, usagef("machine: %s", msg)
	}
	if len(pos) < 2 {
		return fail("expected <command> <name>; use lcm machine -h")
	}
	if f.format != "json" && f.format != "yaml" {
		return fail("-o must be json or yaml")
	}
	if f.limit < 1 || f.limit > 199 {
		return fail("--limit must be between 1 and 199")
	}
	command, name := pos[0], pos[1]
	req := client.Request{Method: http.MethodGet, Path: client.Path("machines", name)}
	var body any
	write, power := false, false
	nargs := 2
	switch command {
	case "observe":
		req.Method, req.Path = http.MethodPost, client.Path("machines", name, "observe")
	case "history", "events", "operations":
		req.Path = client.Path("machines", name, command)
		req.Query = url.Values{"limit": {strconv.Itoa(f.limit)}}
		if f.cursor != "" {
			req.Query.Set("cursor", f.cursor)
		}
	case "capabilities":
		req.Path = client.Path("machines", name, command)
	case "operation", "resolve":
		nargs = 3
		if len(pos) != nargs || strings.TrimSpace(pos[2]) == "" {
			return fail(command + " requires <name> <operation-id>")
		}
		req.Path = client.Path("machines", name, "operations", pos[2])
		if command == "resolve" {
			req.Method, req.Path, write = http.MethodPost, client.Path("machines", name, "operations", pos[2], "resolve"), true
			body = map[string]any{"reason": f.reason}
		}
	case "maintenance":
		req.Path = client.Path("machines", name, "maintenance")
		if len(pos) == 3 {
			nargs = 3
			switch pos[2] {
			case "preview":
				req.Path = client.Path("machines", name, "maintenance", "preview")
			case "enter":
				if len(f.previewHash) != 64 {
					return fail("maintenance enter requires --preview-hash from a reviewed maintenance preview")
				}
				req.Method, write = http.MethodPost, true
				body = map[string]any{"reason": f.reason, "acknowledged": f.acknowledge, "previewHash": f.previewHash}
			case "exit":
				req.Method, req.Path, write = http.MethodPost, client.Path("machines", name, "maintenance", "exit"), true
				body = map[string]any{"reason": f.reason}
			default:
				return fail("maintenance expects <name> [preview|enter|exit]")
			}
		}
	case "reboot", "shutdown":
		req.Method, req.Path, write, power = http.MethodPost, client.Path("machines", name, "operations"), true, true
		if !machineKeyPattern.MatchString(f.key) {
			return fail("power requests require a caller-provided --idempotency-key (1-128 letters, digits, _, ., : or -)")
		}
		action := command
		if command == "shutdown" {
			action = "graceful-shutdown"
		}
		body = map[string]any{"action": action, "idempotencyKey": f.key}
	case "retire", "resume":
		req.Method, req.Path, write = http.MethodPost, client.Path("machines", name, "phase"), true
		phase := "retired"
		if command == "resume" {
			phase = "managed"
		}
		body = map[string]any{"phase": phase}
	case "unmanage":
		req.Method, write = http.MethodDelete, true
	default:
		return fail("unknown command " + strconv.Quote(command) + "; use lcm machine -h")
	}
	if len(pos) != nargs || strings.TrimSpace(name) == "" {
		return fail("unexpected or missing arguments; use lcm machine -h")
	}
	if write {
		if f.version < 1 {
			return fail("writes require --version with the current document version")
		}
		if strings.TrimSpace(f.reason) == "" || len(f.reason) > 1000 || strings.ContainsAny(f.reason, "\x00\r") {
			return fail("writes require a nonempty --reason of at most 1000 bytes without NUL or CR")
		}
		if f.confirm != name || !f.acknowledge {
			return fail("writes require --confirm <name> and --acknowledge for workload coverage, downtime and manual recovery")
		}
		req.Header = http.Header{"If-Match": {strconv.Quote(strconv.Itoa(f.version))}}
	}
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return client.Request{}, false, err
		}
		req.Body, req.ContentType = raw, "application/json"
	}
	return req, power, nil
}
