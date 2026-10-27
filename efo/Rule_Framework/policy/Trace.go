package policy

import (
	rfconfig "1935/efo/Rule_Framework/config"
	"fmt"
	"sort"
	"sync"
	"time"

	i "1935/efo/LCM_Enabler/interfaces"
)

/*
Execution trace.

A run is one trigger event walking the policy DAG. Only the latest run of a
policy is kept: StartRun replaces the current run wholesale, and every mutator
carries the run id it belongs to so that stragglers from a superseded run are
dropped instead of corrupting the new one.

The Recorder deliberately touches no engine state. It never reads or writes a
dataPacket, ListNode.packet or nextPath; every payload handed to it is a value
already materialized in the calling walker's own goroutine. All Recorder state
lives behind its own mutex, so tracing adds no shared access to the engine.

A nil *Recorder is a valid receiver on every method and disables tracing, which
is what a Policy initialized without Deploy (module inner policies, unit tests)
uses.
*/

// Recorder holds the latest execution trace of one policy.
type Recorder struct {
	mu      sync.Mutex
	policy  string
	nextRun int
	cur     *run
}

// run is the mutable state of the latest execution.
type run struct {
	id        string
	trigger   i.TriggerInfo
	startedAt time.Time
	updatedAt time.Time

	// seq numbers retained events; total counts every event including those
	// dropped by the size cap.
	seq       int
	total     int
	events    []i.NodeEvent
	truncated bool

	// inflight counts walker goroutines spawned but not yet returned; blocked
	// counts those parked in a readiness receive. A run can produce no further
	// event once inflight-blocked reaches zero, because only a running walker
	// can release a blocked one.
	inflight int
	blocked  int
	waiting  map[waitKey]int
}

type waitKey struct {
	scope string
	node  string
	port  int
}

// NewRecorder creates a recorder for one policy.
func NewRecorder(policyName string) *Recorder {
	return &Recorder{policy: policyName}
}

// AttachRecorder wires a recorder into every node of the policy, descending into
// modules so their internal nodes report under a scope path. It must be called
// after Init and before any walker goroutine starts, which makes the node fields
// immutable at runtime and therefore lock-free to read.
func (p *Policy) AttachRecorder(rec *Recorder) {
	if p == nil {
		return
	}
	p.trace = rec
	attachToList(p.PolicyList, rec, "")
}

func attachToList(list *PolicyList, rec *Recorder, scope string) {
	if list == nil {
		return
	}
	for name, node := range list.NodeSet {
		if node == nil {
			continue
		}
		node.rec = rec
		node.scope = scope
		if node.baseNode == nil {
			continue
		}
		// A module hosts its own inner policy; its nodes share the outer
		// recorder and report under "<outer scope>/<module node name>".
		if m, ok := (*node.baseNode).(*Module); ok && m.policy != nil {
			child := name
			if scope != "" {
				child = scope + "/" + name
			}
			attachToList(m.policy.PolicyList, rec, child)
		}
	}
}

// StartRun discards the previous run and begins a new one, returning its id.
// The returned id is threaded down the DAG through ExecInfo.RunID.
func (r *Recorder) StartRun(node string, nodeType string, content i.DataPacketContent) string {
	if r == nil {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	r.nextRun++
	id := fmt.Sprintf("run-%d", r.nextRun)
	now := time.Now()
	r.cur = &run{
		id:        id,
		trigger:   i.TriggerInfo{Node: node, NodeType: nodeType, Content: cloneContent(content)},
		startedAt: now,
		updatedAt: now,
		events:    make([]i.NodeEvent, 0, 8),
		waiting:   make(map[waitKey]int),
	}
	return id
}

// current returns the run matching runID, or nil if it has been superseded.
// The caller must hold the mutex.
func (r *Recorder) current(runID string) *run {
	if r.cur == nil || r.cur.id != runID {
		return nil
	}
	return r.cur
}

// Spawn registers a walker goroutine. It must be called by the spawning
// goroutine before the go statement, so a scheduled-but-not-started walker is
// never mistaken for a finished run.
func (r *Recorder) Spawn(runID string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if cur := r.current(runID); cur != nil {
		cur.inflight++
	}
}

// Done deregisters a walker goroutine.
func (r *Recorder) Done(runID string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if cur := r.current(runID); cur != nil {
		cur.inflight--
	}
}

// BlockOn marks a walker as parked on an input port. It must be called before
// the readiness receive.
func (r *Recorder) BlockOn(runID string, scope string, node string, port int) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if cur := r.current(runID); cur != nil {
		cur.blocked++
		cur.waiting[waitKey{scope: scope, node: node, port: port}]++
	}
}

// Unblock clears the mark set by BlockOn.
func (r *Recorder) Unblock(runID string, scope string, node string, port int) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cur := r.current(runID)
	if cur == nil {
		return
	}
	cur.blocked--
	k := waitKey{scope: scope, node: node, port: port}
	if cur.waiting[k] <= 1 {
		delete(cur.waiting, k)
	} else {
		cur.waiting[k]--
	}
}

// Park marks a walker as waiting for a nested walker's result. It affects only
// the completion predicate, not the waiting list, because such a wait is a
// normal handoff rather than a stalled branch the designer needs to see.
func (r *Recorder) Park(runID string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if cur := r.current(runID); cur != nil {
		cur.blocked++
	}
}

// Unpark clears the mark set by Park.
func (r *Recorder) Unpark(runID string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if cur := r.current(runID); cur != nil {
		cur.blocked--
	}
}

// Record appends one node execution to the run, assigning its sequence number,
// converting ev.StartOffsetMs from an absolute UnixMilli timestamp into an
// offset from the start of the run, and cloning the payloads so the stored event
// is detached from anything the caller may later reuse.
func (r *Recorder) Record(runID string, ev i.NodeEvent) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cur := r.current(runID)
	if cur == nil {
		return
	}

	cur.total++
	cur.updatedAt = time.Now()
	if len(cur.events) >= rfconfig.TraceMaxEvents() {
		cur.truncated = true
		return
	}
	cur.seq++
	ev.Seq = cur.seq
	// Callers pass an absolute start timestamp; only the recorder knows when the
	// run began, so the conversion to a run-relative offset happens here.
	ev.StartOffsetMs = ev.StartOffsetMs - cur.startedAt.UnixMilli()
	// Clone here rather than at the call site so the detachment guarantee holds
	// for every caller, present and future.
	ev.Input = cloneAnySlice(ev.Input)
	ev.InputRaw = cloneContentSlice(ev.InputRaw)
	ev.InputErrors = cloneStrings(ev.InputErrors)
	ev.Output = cloneOutput(ev.Output)
	ev.Ports = clonePorts(ev.Ports)
	cur.events = append(cur.events, ev)
}

// Snapshot returns a detached view of the latest run, keeping only events with
// Seq greater than since.
func (r *Recorder) Snapshot(since int) i.RunTrace {
	if r == nil {
		return emptyTrace("")
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.cur == nil {
		return emptyTrace(r.policy)
	}
	cur := r.cur

	events := make([]i.NodeEvent, 0, len(cur.events))
	for _, ev := range cur.events {
		if ev.Seq > since {
			events = append(events, ev)
		}
	}

	waiting := make([]i.WaitingNode, 0, len(cur.waiting))
	for k, n := range cur.waiting {
		if n > 0 {
			waiting = append(waiting, i.WaitingNode{Node: k.node, Scope: k.scope, Port: k.port})
		}
	}
	sort.Slice(waiting, func(a, b int) bool {
		if waiting[a].Scope != waiting[b].Scope {
			return waiting[a].Scope < waiting[b].Scope
		}
		if waiting[a].Node != waiting[b].Node {
			return waiting[a].Node < waiting[b].Node
		}
		return waiting[a].Port < waiting[b].Port
	})

	active := cur.inflight - cur.blocked
	status := "running"
	if active <= 0 {
		status = "complete"
	}

	return i.RunTrace{
		Policy:     r.policy,
		HasRun:     true,
		RunID:      cur.id,
		Trigger:    cur.trigger,
		StartedAt:  cur.startedAt.Format(time.RFC3339Nano),
		UpdatedAt:  cur.updatedAt.Format(time.RFC3339Nano),
		DurationMs: cur.updatedAt.Sub(cur.startedAt).Milliseconds(),
		Status:     status,
		Active:     active,
		Blocked:    cur.blocked,
		Waiting:    waiting,
		EventCount: cur.total,
		Since:      since,
		Truncated:  cur.truncated,
		Events:     events,
	}
}

// LatestRun returns the policy's latest execution trace.
func (p *Policy) LatestRun(since int) i.RunTrace {
	if p == nil {
		return emptyTrace("")
	}
	trace := p.trace.Snapshot(since)
	if trace.Policy == "" {
		trace.Policy = p.Description.PolicyName
	}
	trace.Since = since
	return trace
}

func emptyTrace(policyName string) i.RunTrace {
	return i.RunTrace{
		Policy:  policyName,
		HasRun:  false,
		Status:  "no-run",
		Waiting: []i.WaitingNode{},
		Events:  []i.NodeEvent{},
	}
}

/*
 * payload cloning
 *
 * One level deep is enough: every map in flight is freshly allocated per call by
 * transformContent, EncodeOutput or the webhook body decode, and none is mutated
 * in place after publication. Cloning also de-aliases output ports, which
 * EncodeOutput fills with the same map object. Note transformContent's
 * pass-through branch copies deliberately for this reason -- turning it into a
 * bare `return src` would falsify the freshly-allocated invariant above.
 */

func cloneContent(c i.DataPacketContent) i.DataPacketContent {
	if c == nil {
		return nil
	}
	out := make(i.DataPacketContent, len(c))
	for k, v := range c {
		out[k] = v
	}
	return out
}

func cloneAny(v any) any {
	switch m := v.(type) {
	case i.DataPacketContent:
		return cloneContent(m)
	case map[string]any:
		return map[string]any(cloneContent(m))
	default:
		return v
	}
}

func cloneAnySlice(in []any) []any {
	if in == nil {
		return []any{}
	}
	out := make([]any, len(in))
	for idx, v := range in {
		out[idx] = cloneAny(v)
	}
	return out
}

func cloneOutput(out i.NodeOutput) i.NodeOutput {
	if out == nil {
		return i.NodeOutput{}
	}
	res := make(i.NodeOutput, len(out))
	for idx, c := range out {
		res[idx] = cloneContent(c)
	}
	return res
}

func cloneContentSlice(in []i.DataPacketContent) []i.DataPacketContent {
	if in == nil {
		return []i.DataPacketContent{}
	}
	out := make([]i.DataPacketContent, len(in))
	for idx, c := range in {
		out[idx] = cloneContent(c)
	}
	return out
}

func cloneStrings(in []string) []string {
	out := make([]string, len(in))
	copy(out, in)
	return out
}

func clonePorts(ports []int) []int {
	out := make([]int, len(ports))
	copy(out, ports)
	return out
}

// triggerContent extracts the payload a trigger pushed into the policy.
func triggerContent(out i.NodeOutput) i.DataPacketContent {
	if len(out) == 0 {
		return nil
	}
	return cloneContent(out[0])
}
