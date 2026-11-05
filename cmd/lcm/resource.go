package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
	"golang.org/x/term"

	"github.com/ACaccel/lcm/internal/client"
)

const resourceUsage = `Usage: lcm resource <command> <name> [args] [flags]

Commands:
  action <name> <action>      Run a service action (deploy, rollback, start, stop,
                              restart, backup, run-job); --version <commit> (deploy),
                              --job <job> (run-job), --dry-run, --wait
  observe <name>              Collect a read-only observation (machine, router)
  history | events <name>     Read a page of observations or events (--limit, --cursor)
  capabilities <name>         Show durable operations and their prerequisites
  maintenance <name> [preview|enter|exit]  Read, enter or exit machine maintenance
  operations <name>           List durable operations (--limit, --cursor)
  operation <name> <id>       Read a durable operation
  reboot | shutdown <name>    Submit a power request; acceptance is not completion
  resolve <name> <id>         Close an operation with an unverified outcome
  retire | resume | unmanage <name>  Change the management phase
  impact <name>               Show the resources that depend on this one or are
                              controlled through it
  group-action <action>       Run a service action on the managed services matching
                              every --label (repeatable k=v; --type service), in
                              dependency order (dependents first for stop); stops
                              at the first failure; --dry-run, --wait

A disruptive action (stop, restart, deploy, rollback, run-job, retiring a
service, reboot, shutdown) on a resource other managed resources depend on or
are controlled through is refused unless --acknowledge-impact is given; review
the impact with "impact" or --dry-run first.

Writes (maintenance enter/exit, reboot, shutdown, resolve, retire, resume,
unmanage) require --reason and --confirm <name>; the current document version
is read from the server. A machine reboot or shutdown enters maintenance itself
unless it is already active. Reboot, shutdown, maintenance enter, retire and
unmanage print what they affect first and ask before sending; --yes skips the
question, which scripts need because a stdin that is not a terminal is refused.
The request acknowledges partial workload coverage, controller downtime and
manual recovery.

Power requests print their idempotency key and version before sending. After an
ambiguous response, inspect operations and retry only with that same
--idempotency-key and --version; a new key could request a second operation. No
command cancels an OS power request. Resolve does not exit maintenance. Retiring
a running service stops it first when its runtime can.
`

var idempotencyKeyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)

type resourceFlags struct {
	// version is the document version (If-Match) for writes, or the commit
	// to deploy for "action ... deploy": both are spelled --version. Writes
	// read the current version when it is not given.
	version   string
	reason    string
	confirm   string
	yes       bool
	key       string
	limit     int
	cursor    string
	format    string
	job       string
	dryRun    bool
	wait      bool
	ackImpact bool
	// typ and labels select the services of group-action.
	typ    string
	labels map[string]string
}

func (a *app) resource(ctx context.Context, args []string) error {
	fs, conn := a.clientFlags("resource")
	fs.Usage = func() { _, _ = io.WriteString(a.stderr, resourceUsage); fs.PrintDefaults() }
	var f resourceFlags
	fs.StringVar(&f.version, "version", "", "document version a write applies to (If-Match), read from the server when omitted; the commit to deploy for action deploy")
	fs.StringVar(&f.reason, "reason", "", "reason for a phase, maintenance or power change")
	fs.StringVar(&f.confirm, "confirm", "", "confirm the exact target resource name")
	fs.BoolVar(&f.yes, "yes", false, "proceed without asking after printing what a write affects")
	fs.StringVar(&f.key, "idempotency-key", "", "power request key, generated when omitted; reuse the printed key to retry an ambiguous request")
	fs.IntVar(&f.limit, "limit", 100, "page size (1-199)")
	fs.StringVar(&f.cursor, "cursor", "", "opaque cursor from the previous page")
	fs.StringVar(&f.format, "o", "json", "output format: json or yaml")
	fs.StringVar(&f.job, "job", "", "name of the job to run (action run-job only)")
	fs.BoolVar(&f.dryRun, "dry-run", false, "show the plan of an action without performing it")
	fs.BoolVar(&f.wait, "wait", false, "wait for an action to finish; exits with status 1 when it failed")
	fs.BoolVar(&f.ackImpact, "acknowledge-impact", false, "accept the impact on dependents and control dependents of a disruptive action")
	fs.StringVar(&f.typ, "type", "", "resource type of group-action (service)")
	f.labels = map[string]string{}
	fs.Func("label", "label selector k=v of group-action (repeatable)", func(v string) error {
		k, val, ok := strings.Cut(v, "=")
		if !ok || k == "" {
			return fmt.Errorf("want key=value, got %q", v)
		}
		f.labels[k] = val
		return nil
	})
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 && pos[0] == "action" {
		return a.resourceAction(ctx, conn, pos, f)
	}
	if len(pos) > 0 && pos[0] == "group-action" {
		return a.groupAction(ctx, conn, pos, f)
	}
	call, err := resourceRequest(pos, f)
	if err != nil {
		return err
	}
	cl, err := a.newClient(conn)
	if err != nil {
		return err
	}
	if call.write {
		if err := a.prepareWrite(ctx, cl, &call, f); err != nil {
			return err
		}
	}
	req, err := call.request()
	if err != nil {
		return err
	}
	power := call.power
	retry := ""
	if power {
		retry = fmt.Sprintf("retry only with --idempotency-key %s --version %d", call.key, call.version)
	}
	resp, err := cl.Do(ctx, req)
	if err != nil {
		if power {
			return fmt.Errorf("%w; if submission is ambiguous, inspect operations and %s", apiError(err), retry)
		}
		return apiError(err)
	}
	operationResponse := power || pos[0] == "operation" || pos[0] == "resolve"
	if len(resp.Body) == 0 {
		if power {
			return fmt.Errorf("server returned no operation ID or state; inspect operations, then %s", retry)
		}
		if operationResponse {
			return fmt.Errorf("server returned no operation ID or state; inspect operations before retrying")
		}
		maintenanceExit := pos[0] == "maintenance" && len(pos) == 3 && pos[2] == "exit"
		if !maintenanceExit {
			return fmt.Errorf("server returned an empty response")
		}
		_, err = fmt.Fprintf(a.stdout, "%s %s: request accepted\n", pos[0], pos[1])
		return err
	}
	var value any
	if err := json.Unmarshal(resp.Body, &value); err != nil {
		if power {
			return fmt.Errorf("decode response: %w; inspect operations, then %s", err, retry)
		}
		return fmt.Errorf("decode response: %w", err)
	}
	operationState := ""
	if operationResponse {
		op, ok := value.(map[string]any)
		id, _ := op["id"].(string)
		state, _ := op["state"].(string)
		if !ok || strings.TrimSpace(id) == "" || strings.TrimSpace(state) == "" {
			if power {
				return fmt.Errorf("server returned an operation without ID or state; inspect operations, then %s", retry)
			}
			return fmt.Errorf("server returned an operation without ID or state; inspect operations before retrying")
		}
		operationState = state
	}
	if err := a.printValue(value, f.format); err != nil {
		return err
	}
	if power && (operationState == "failed" || operationState == "cancelled") {
		return fmt.Errorf("power operation is %s; inspect the operation before taking further action", operationState)
	}
	return nil
}

// printValue writes decoded JSON as indented JSON or YAML.
func (a *app) printValue(value any, format string) error {
	if format == "yaml" {
		enc := yaml.NewEncoder(a.stdout)
		enc.SetIndent(2)
		if err := enc.Encode(value); err != nil {
			return err
		}
		return enc.Close()
	}
	enc := json.NewEncoder(a.stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(value)
}

// resourceCall is the request of one resource command. prepareWrite
// completes a write: the version, the preview and question, and the key of
// a power request.
type resourceCall struct {
	req  client.Request
	body map[string]any
	name string
	// write needs a version, reason and confirmation; power is a power
	// submission, whose errors need retry guidance.
	write, power bool
	// impact is the action whose impact is printed before asking; enter is
	// a maintenance entry, which prints the maintenance preview and sends
	// its hash.
	impact string
	enter  bool
	// version is If-Match: --version, or the current version once read.
	version int
	key     string
}

// asks reports whether the write prints a preview and asks before sending.
func (c resourceCall) asks() bool { return c.impact != "" || c.enter }

// request returns the HTTP request with its body encoded.
func (c resourceCall) request() (client.Request, error) {
	req := c.req
	if c.write {
		req.Header = http.Header{"If-Match": {strconv.Quote(strconv.Itoa(c.version))}}
	}
	if c.body != nil {
		raw, err := json.Marshal(c.body)
		if err != nil {
			return client.Request{}, err
		}
		req.Body, req.ContentType = raw, "application/json"
	}
	return req, nil
}

// resourceRequest builds the call of every command except action and
// group-action, checking the flags before anything is sent.
func resourceRequest(pos []string, f resourceFlags) (resourceCall, error) {
	fail := func(msg string) (resourceCall, error) {
		return resourceCall{}, usagef("resource: %s", msg)
	}
	if len(pos) < 2 {
		return fail("expected <command> <name>; use lcm resource -h")
	}
	if f.format != "json" && f.format != "yaml" {
		return fail("-o must be json or yaml")
	}
	if f.limit < 1 || f.limit > 199 {
		return fail("--limit must be between 1 and 199")
	}
	command, name := pos[0], pos[1]
	req := client.Request{Method: http.MethodGet, Path: client.Path("resources", name)}
	var body map[string]any
	write, power, enter, impact := false, false, false, ""
	nargs := 2
	switch command {
	case "observe":
		req.Method, req.Path = http.MethodPost, client.Path("resources", name, "observe")
	case "history", "events", "operations":
		req.Path = client.Path("resources", name, command)
		req.Query = url.Values{"limit": {strconv.Itoa(f.limit)}}
		if f.cursor != "" {
			req.Query.Set("cursor", f.cursor)
		}
	case "capabilities", "impact":
		req.Path = client.Path("resources", name, command)
	case "operation", "resolve":
		nargs = 3
		if len(pos) != nargs || strings.TrimSpace(pos[2]) == "" {
			return fail(command + " requires <name> <operation-id>")
		}
		req.Path = client.Path("resources", name, "operations", pos[2])
		if command == "resolve" {
			req.Method, req.Path, write = http.MethodPost, client.Path("resources", name, "operations", pos[2], "resolve"), true
			body = map[string]any{"reason": f.reason}
		}
	case "maintenance":
		req.Path = client.Path("resources", name, "maintenance")
		if len(pos) == 3 {
			nargs = 3
			switch pos[2] {
			case "preview":
				req.Path = client.Path("resources", name, "maintenance", "preview")
			case "enter":
				req.Method, write, enter = http.MethodPost, true, true
				body = map[string]any{"reason": f.reason}
			case "exit":
				req.Method, req.Path, write = http.MethodPost, client.Path("resources", name, "maintenance", "exit"), true
				body = map[string]any{"reason": f.reason}
			default:
				return fail("maintenance expects <name> [preview|enter|exit]")
			}
		}
	case "reboot", "shutdown":
		req.Method, req.Path, write, power = http.MethodPost, client.Path("resources", name, "operations"), true, true
		if f.key != "" && !idempotencyKeyPattern.MatchString(f.key) {
			return fail("--idempotency-key must be 1-128 letters, digits, _, ., : or -, starting with a letter or digit")
		}
		impact = command
		if command == "shutdown" {
			impact = "graceful-shutdown"
		}
		body = map[string]any{"action": impact, "reason": f.reason, "confirm": f.confirm}
		if f.ackImpact {
			body["acknowledgeImpact"] = true
		}
	case "retire", "resume", "unmanage":
		req.Method, req.Path, write = http.MethodPost, client.Path("resources", name, "phase"), true
		phase := map[string]string{"retire": "retired", "resume": "managed", "unmanage": "unmanaged"}[command]
		body = map[string]any{"phase": phase, "reason": f.reason}
		if command != "resume" {
			impact = command
		}
		if f.ackImpact {
			body["acknowledgeImpact"] = true
		}
	default:
		return fail("unknown command " + strconv.Quote(command) + "; use lcm resource -h")
	}
	if len(pos) != nargs || strings.TrimSpace(name) == "" {
		return fail("unexpected or missing arguments; use lcm resource -h")
	}
	call := resourceCall{req: req, body: body, name: name, write: write, power: power, impact: impact, enter: enter}
	if write {
		if f.version != "" {
			version, err := strconv.Atoi(f.version)
			if err != nil || version < 1 {
				return fail("--version must be a document version of at least 1")
			}
			call.version = version
		}
		if strings.TrimSpace(f.reason) == "" || len(f.reason) > 1000 || strings.ContainsAny(f.reason, "\x00\r") {
			return fail("writes require a nonempty --reason of at most 1000 bytes without NUL or CR")
		}
		if f.confirm != name {
			return fail("writes require --confirm with the exact resource name")
		}
	}
	return call, nil
}

// prepareWrite completes a write: it reads the current version unless
// --version was given, prints what the write affects and asks before it is
// sent, and gives a power request its idempotency key.
func (a *app) prepareWrite(ctx context.Context, cl *client.Client, c *resourceCall, f resourceFlags) error {
	var cur struct {
		Type    string `json:"type"`
		Version int    `json:"version"`
	}
	if c.version == 0 || c.asks() {
		if err := cl.JSON(ctx, http.MethodGet, client.Path("resources", c.name), nil, nil, &cur); err != nil {
			return apiError(err)
		}
	}
	if c.version == 0 {
		c.version = cur.Version
	}
	if c.impact != "" {
		var imp impactView
		if err := cl.JSON(ctx, http.MethodGet, client.Path("resources", c.name, "impact"), url.Values{"action": {c.impact}}, nil, &imp); err != nil {
			return apiError(err)
		}
		if len(imp.Dependents)+len(imp.ControlDependents) == 0 {
			_, _ = fmt.Fprintln(a.stderr, "impact: no other managed resource depends on or is controlled through it")
		}
		a.printImpact(a.stderr, "", imp)
		if imp.AcknowledgementRequired && !f.ackImpact {
			return usagef("resource: %s %s reaches the resources above; add --acknowledge-impact", c.impact, c.name)
		}
	}
	if c.enter || (c.power && cur.Type == "machine") {
		if err := a.printMaintenance(ctx, cl, c); err != nil {
			return err
		}
	}
	if c.power {
		c.key = f.key
		if c.key == "" {
			var b [16]byte
			if _, err := rand.Read(b[:]); err != nil {
				return err
			}
			c.key = "cli-" + hex.EncodeToString(b[:])
		}
		c.body["idempotencyKey"] = c.key
		_, _ = fmt.Fprintf(a.stderr, "request: %s %s at version %d, idempotency key %s\n", c.body["action"], c.name, c.version, c.key)
		_, _ = fmt.Fprintln(a.stderr, "acceptance is not completion; the request cannot be cancelled once the OS or router accepts it")
	}
	if c.req.Path == client.Path("resources", c.name, "phase") {
		c.body["version"] = c.version
	}
	if c.asks() {
		return a.confirmWrite(f.yes)
	}
	return nil
}

// printMaintenance prints the maintenance a write enters or uses: the
// active maintenance a power request reuses, or the preview of a new one,
// whose hash an entry sends so that it is refused if the workloads change.
func (a *app) printMaintenance(ctx context.Context, cl *client.Client, c *resourceCall) error {
	if c.power {
		var st struct {
			Maintenance *struct {
				Reason    string    `json:"reason"`
				EnteredAt time.Time `json:"enteredAt"`
				Actor     struct {
					Name string `json:"name"`
				} `json:"actor"`
			} `json:"maintenance"`
		}
		if err := cl.JSON(ctx, http.MethodGet, client.Path("resources", c.name, "maintenance"), nil, nil, &st); err != nil {
			return apiError(err)
		}
		if m := st.Maintenance; m != nil {
			_, _ = fmt.Fprintf(a.stderr, "maintenance: active since %s by %s (%s); the request keeps it\n", m.EnteredAt.Local().Format(time.DateTime), m.Actor.Name, m.Reason)
			return nil
		}
	}
	var p struct {
		Hash      string `json:"hash"`
		Workloads []struct {
			Name    string `json:"name"`
			Version int    `json:"version"`
			State   string `json:"state"`
		} `json:"workloads"`
	}
	if err := cl.JSON(ctx, http.MethodGet, client.Path("resources", c.name, "maintenance", "preview"), nil, nil, &p); err != nil {
		return apiError(err)
	}
	if c.power {
		_, _ = fmt.Fprintln(a.stderr, "maintenance: the request enters maintenance, which holds local writes until an explicit exit")
	} else {
		_, _ = fmt.Fprintln(a.stderr, "maintenance: holds local writes until an explicit exit; it stops nothing that runs")
	}
	_, _ = fmt.Fprintln(a.stderr, "known services (coverage is partial; LCM is offline while the machine is down; power-on is manual):")
	if len(p.Workloads) == 0 {
		_, _ = fmt.Fprintln(a.stderr, "  none")
	}
	for _, w := range p.Workloads {
		_, _ = fmt.Fprintf(a.stderr, "  %s v%d %s\n", w.Name, w.Version, w.State)
	}
	if c.enter {
		c.body["previewHash"] = p.Hash
	}
	return nil
}

// confirmWrite asks before a write whose effect was printed. Without --yes
// a stdin that is not a terminal refuses, so that a script never sends a
// write nobody reviewed.
func (a *app) confirmWrite(yes bool) error {
	if yes {
		return nil
	}
	if !a.interactive() {
		return usagef("resource: stdin is not a terminal; review the output above and add --yes to proceed")
	}
	_, _ = fmt.Fprint(a.stderr, "Proceed? [y/N] ")
	line, err := bufio.NewReader(a.stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("read answer: %w", err)
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return nil
	}
	return errors.New("cancelled; nothing was sent")
}

// interactive reports whether stdin is a terminal a person can answer on.
func (a *app) interactive() bool {
	if a.terminal != nil {
		return a.terminal()
	}
	f, ok := a.stdin.(*os.File)
	return ok && term.IsTerminal(int(f.Fd())) //nolint:gosec // file descriptors fit in int
}

// resourceAction runs a service lifecycle action, optionally waiting for
// its outcome. The server answers 202 and reports progress under /actions.
func (a *app) resourceAction(ctx context.Context, conn *connection, pos []string, f resourceFlags) error {
	if len(pos) != 3 {
		return usagef("resource: action expects <name> <action>")
	}
	body := map[string]any{}
	if f.version != "" {
		body["version"] = f.version
	}
	if f.job != "" {
		body["job"] = f.job
	}
	if f.ackImpact {
		body["acknowledgeImpact"] = true
	}
	cl, err := a.newClient(conn)
	if err != nil {
		return err
	}
	name, act := pos[1], pos[2]
	path := client.Path("resources", name, "actions", act)
	if f.dryRun {
		var plan struct {
			From   string     `json:"from"`
			To     string     `json:"to"`
			Steps  []string   `json:"steps"`
			Impact impactView `json:"impact"`
		}
		if err := cl.JSON(ctx, "POST", path, url.Values{"dryRun": {"true"}}, body, &plan); err != nil {
			return apiError(err)
		}
		_, _ = fmt.Fprintf(a.stdout, "plan: %s %s (%s -> %s)\n", act, name, plan.From, plan.To)
		for i, s := range plan.Steps {
			_, _ = fmt.Fprintf(a.stdout, "  %d. %s\n", i+1, s)
		}
		a.printImpact(a.stdout, "  ", plan.Impact)
		return nil
	}
	var started struct {
		ActionID string `json:"actionId"`
	}
	if err := cl.JSON(ctx, "POST", path, nil, body, &started); err != nil {
		return apiError(err)
	}
	_, _ = fmt.Fprintf(a.stdout, "action %s started (%s %s)\n", started.ActionID, act, name)
	if !f.wait {
		return nil
	}
	return a.waitAction(ctx, cl, started.ActionID)
}

func (a *app) waitAction(ctx context.Context, cl *client.Client, id string) error {
	for {
		var act struct {
			Status string `json:"status"`
			Error  string `json:"error"`
			Result *struct {
				From            string `json:"from"`
				To              string `json:"to"`
				Version         string `json:"version"`
				PreviousVersion string `json:"previousVersion"`
				DurationMs      int64  `json:"durationMs"`
			} `json:"result"`
		}
		if err := cl.JSON(ctx, "GET", client.Path("actions", id), nil, nil, &act); err != nil {
			return apiError(err)
		}
		switch act.Status {
		case "succeeded":
			line := fmt.Sprintf("action %s succeeded", id)
			if r := act.Result; r != nil {
				line += fmt.Sprintf(" (%s -> %s", r.From, r.To)
				if r.Version != "" {
					line += ", version " + r.Version
				}
				line += fmt.Sprintf(", %s)", (time.Duration(r.DurationMs) * time.Millisecond).Round(time.Millisecond))
			}
			_, err := fmt.Fprintln(a.stdout, line)
			return err
		case "failed":
			return fmt.Errorf("action %s failed: %s", id, act.Error)
		}
		if err := sleep(ctx, a.pollInterval()); err != nil {
			return fmt.Errorf("stopped waiting for action %s: %w", id, err)
		}
	}
}

// impactView is the part of a ResourceImpact the CLI prints.
type impactView struct {
	Dependents []struct {
		Name     string `json:"name"`
		Relation string `json:"relation"`
		Via      string `json:"via"`
	} `json:"dependents"`
	ControlDependents []struct {
		Name string `json:"name"`
		Via  string `json:"via"`
	} `json:"controlDependents"`
	AcknowledgementRequired bool `json:"acknowledgementRequired"`
}

func (a *app) printImpact(w io.Writer, indent string, imp impactView) {
	for _, d := range imp.Dependents {
		_, _ = fmt.Fprintf(w, "%simpact: %s (%s %s)\n", indent, d.Name, d.Relation, d.Via)
	}
	for _, d := range imp.ControlDependents {
		_, _ = fmt.Fprintf(w, "%simpact: %s (controlPath %s)\n", indent, d.Name, d.Via)
	}
	if imp.AcknowledgementRequired {
		_, _ = fmt.Fprintf(w, "%srequires --acknowledge-impact\n", indent)
	}
}

// groupAction runs one service action on the services a label selector
// matches. The server answers 202 and reports progress under
// /resource-groups/actions/{id}.
func (a *app) groupAction(ctx context.Context, conn *connection, pos []string, f resourceFlags) error {
	if len(pos) != 2 {
		return usagef("resource: group-action expects <action>")
	}
	if len(f.labels) == 0 {
		return usagef("resource: group-action requires at least one --label key=value")
	}
	if f.typ != "" && f.typ != "service" {
		return usagef("resource: group-action runs service actions only (--type service)")
	}
	params := map[string]any{}
	if f.version != "" {
		params["version"] = f.version
	}
	if f.job != "" {
		params["job"] = f.job
	}
	selector := map[string]any{"labels": f.labels}
	if f.typ != "" {
		selector["type"] = f.typ
	}
	body := map[string]any{"selector": selector, "action": pos[1], "params": params, "dryRun": f.dryRun, "acknowledgeImpact": f.ackImpact}
	cl, err := a.newClient(conn)
	if err != nil {
		return err
	}
	path := client.Path("resource-groups", "actions")
	if f.dryRun {
		var plan struct {
			Steps []struct {
				Resource string `json:"resource"`
				Plan     *struct {
					From string `json:"from"`
					To   string `json:"to"`
				} `json:"plan"`
				Error  string     `json:"error"`
				Impact impactView `json:"impact"`
			} `json:"steps"`
		}
		if err := cl.JSON(ctx, "POST", path, nil, body, &plan); err != nil {
			return apiError(err)
		}
		for i, st := range plan.Steps {
			switch {
			case st.Error != "":
				_, _ = fmt.Fprintf(a.stdout, "%d. %s %s: cannot run: %s\n", i+1, pos[1], st.Resource, st.Error)
			case st.Plan != nil:
				_, _ = fmt.Fprintf(a.stdout, "%d. %s %s (%s -> %s)\n", i+1, pos[1], st.Resource, st.Plan.From, st.Plan.To)
			}
			a.printImpact(a.stdout, "   ", st.Impact)
		}
		return nil
	}
	var started struct {
		ID string `json:"id"`
	}
	if err := cl.JSON(ctx, "POST", path, nil, body, &started); err != nil {
		return apiError(err)
	}
	_, _ = fmt.Fprintf(a.stdout, "group action %s started (%s)\n", started.ID, pos[1])
	if !f.wait {
		return nil
	}
	for {
		var g struct {
			Status string `json:"status"`
			Steps  []struct {
				Resource string `json:"resource"`
				Status   string `json:"status"`
				Error    string `json:"error"`
			} `json:"steps"`
		}
		if err := cl.JSON(ctx, "GET", client.Path("resource-groups", "actions", started.ID), nil, nil, &g); err != nil {
			return apiError(err)
		}
		if g.Status != "running" {
			for _, st := range g.Steps {
				line := fmt.Sprintf("  %s: %s", st.Resource, st.Status)
				if st.Error != "" {
					line += " (" + st.Error + ")"
				}
				_, _ = fmt.Fprintln(a.stdout, line)
			}
			if g.Status == "failed" {
				return fmt.Errorf("group action %s failed", started.ID)
			}
			_, err := fmt.Fprintf(a.stdout, "group action %s succeeded\n", started.ID)
			return err
		}
		if err := sleep(ctx, a.pollInterval()); err != nil {
			return fmt.Errorf("stopped waiting for group action %s: %w", started.ID, err)
		}
	}
}
