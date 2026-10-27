/*
Package CALM_PlacementApply is the acting half of stage 3 of the CALM loop: it
turns the placement plan the policy mode produced into control-plane calls.

Four input ports, one per policy mode, joined with JoinAny. Exactly one mode
branch runs per slot, so a node waiting for all four would wait forever. The
engine marks every output port of a branching node ready regardless of the
branch actually taken, which means the three ports whose branch did not run may
still hold content left over from an earlier run; the only port read here is
ExecInfo.ActiveInPort.

Only the replica targets of the plan are acted on, through the same read-then-
write sequence IEEE1935_Resourceoptimizer uses: read the current deployment,
then PATCH the service with its CPU and memory unchanged and the replica count
set to the target. Migrations and offloads have no testbed counterpart; a
non-empty set is reported as unapplied_actions rather than dropped in silence.

Every outcome -- applied, skipped, failed, unapplied -- lands in the output map,
so the execution trace shows what the slot did without reading the EFO log. One
service failing does not stop the others: a partial apply is reported, not
hidden behind an early return.
*/
package CALM_PlacementApply

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	lcm "1935/efo/LCM_Enabler/interfaces"
	"1935/efo/Rule_Framework/config"
	i "1935/efo/Rule_Framework/interfaces"
	"1935/efo/Rule_Framework/nodes/Functions/calm"
	u "1935/efo/Rule_Framework/nodes/utils"
)

// Property holds the node's settings.
type Property struct {
	// DryRun computes and reports every change without issuing a single PATCH.
	// It is what makes a demo rehearsal, or a first run against a live cluster,
	// safe to execute.
	DryRun bool `yaml:"dry_run"`
	// TargetCluster overrides the decision domain. The cluster normally arrives
	// on the payload as target_cluster, published by the telemetry stage; this
	// is the manual override for a graph where that field is not on the wire.
	TargetCluster string `yaml:"target_cluster"`
}

// Output field keys. They are the node's wire contract: the post-verification
// stage and the GUI read them by name.
const (
	KeyApplyOK          = "apply_ok"
	KeyMode             = "mode"
	KeyTargetCluster    = "target_cluster"
	KeyDryRun           = "dry_run"
	KeySkipped          = "skipped"
	KeyErrors           = "errors"
	KeyUnappliedActions = "unapplied_actions"

	// AppliedPrefix prefixes one dynamic key per changed service, holding
	// "old->new". The key set depends on the plan, so it cannot be a struct
	// field and is added to the encoded output by hand.
	AppliedPrefix = "applied_"
)

// Input field keys read from the active port.
const (
	// inPlanPrefix and inPlanReplicasSuffix bracket the flattened replica
	// target the policy mode publishes as plan_<service>_replicas. The nested
	// calm.KeyReplicaTarget object is preferred when present; this is the
	// fallback for the flattened form.
	inPlanPrefix         = "plan_"
	inPlanReplicasSuffix = "_replicas"
)

// OutputFormat is the fixed part of the output. The dynamic applied_<service>
// keys are added on top of it.
//
// The tags are mapstructure tags rather than yaml ones because that is what the
// port encoder and the design-time schema both read for a port format.
type OutputFormat struct {
	// ApplyOK reports that no service failed. It is not weakened by
	// UnappliedActions: an unimplemented migration is a plan the node cannot
	// carry out, not an apply that went wrong, and it has its own field.
	ApplyOK          bool     `mapstructure:"apply_ok"`
	Mode             string   `mapstructure:"mode"`
	TargetCluster    string   `mapstructure:"target_cluster"`
	DryRun           bool     `mapstructure:"dry_run"`
	Skipped          []string `mapstructure:"skipped"`
	Errors           []string `mapstructure:"errors"`
	UnappliedActions []string `mapstructure:"unapplied_actions"`
}

var Description = i.NodeDescription{
	NodeType:      "CALM_PlacementApply",
	NumInputPort:  4,
	NumOutputPort: 1,
	// Fixed rather than decided per instance: this node never has a reason to
	// wait for all four modes, since only one of them can ever run.
	Join: i.JoinAny,
}

type CALM_PlacementApply struct {
	i.FunctionNode
}

// currentDeployment is the control plane's deployment record. Alocated_memory
// is the upstream JSON spelling in Edge_Inventory.Deployment, not a typo here:
// matching it is what makes the memory value survive the round trip instead of
// being PATCHed back as an empty string.
type currentDeployment struct {
	Replicas        string `json:"Replicas"`
	AllocatedCPU    string `json:"Allocated_cpu"`
	AllocatedMemory string `json:"Alocated_memory"`
}

func (n *CALM_PlacementApply) Execute(input []any, info i.ExecInfo) i.NodeExecResult {
	content := activeContent(input, info.ActiveInPort)

	var property Property
	// The property fill sees only the active port. Handing it the whole input
	// slice would let a stale packet on a branch that did not run fill a field
	// the configuration left empty.
	u.DecodeProperty(Description.NodeType, n.Config, []any{content}, &property)

	ctx, cancel := u.RunContext(info)
	defer cancel()

	out := OutputFormat{
		DryRun:           property.DryRun,
		Skipped:          []string{},
		Errors:           []string{},
		UnappliedActions: []string{},
	}
	applied := map[string]string{}

	if content == nil {
		out.Errors = append(out.Errors, fmt.Sprintf("no payload on active input port %d", info.ActiveInPort))
		return emit(out, applied)
	}

	// The mode is read before the plan so a malformed plan is still reported
	// against the mode that produced it.
	out.Mode = stringAt(content, calm.KeyMode)

	plan, err := decodePlan(content)
	if err != nil {
		out.Errors = append(out.Errors, err.Error())
		return emit(out, applied)
	}
	if plan.Mode != "" {
		out.Mode = plan.Mode
	}
	out.UnappliedActions = unappliedActions(plan)

	out.TargetCluster = property.TargetCluster
	if out.TargetCluster == "" {
		out.TargetCluster = stringAt(content, KeyTargetCluster)
	}
	if out.TargetCluster == "" {
		out.Errors = append(out.Errors, fmt.Sprintf(
			"target cluster unknown: payload carries no %q and the target_cluster property is empty", KeyTargetCluster))
		return emit(out, applied)
	}

	baseURL := info.ControlPlaneBaseURL
	if baseURL == "" {
		baseURL = config.ControlPlaneBaseURL()
	}
	client := &http.Client{Timeout: time.Duration(config.HTTPTimeoutSeconds() * float64(time.Second))}

	// Services are walked in name order: Go randomises map iteration, so
	// ranging the plan directly would reorder the trace, the logs and the PATCH
	// sequence between two runs of the same plan.
	for _, service := range sortedNames(plan.ReplicaTarget) {
		change, skipped, err := applyService(
			ctx, client, baseURL, out.TargetCluster, service, plan.ReplicaTarget[service], property.DryRun)
		switch {
		case err != nil:
			out.Errors = append(out.Errors, fmt.Sprintf("%s: %v", service, err))
		case skipped:
			out.Skipped = append(out.Skipped, service)
		default:
			applied[service] = change
		}
	}

	out.ApplyOK = len(out.Errors) == 0
	return emit(out, applied)
}

// applyService brings one service to its target replica count and reports what
// it did: the "old->new" change, or that the service was already at the target.
func applyService(
	ctx context.Context,
	client *http.Client,
	baseURL, cluster, service string,
	target int,
	dryRun bool,
) (change string, skipped bool, err error) {
	if target < 0 {
		return "", false, fmt.Errorf("target replica count %d is negative", target)
	}

	deployment, err := fetchDeployment(ctx, client, baseURL, cluster, service)
	if err != nil {
		return "", false, err
	}
	current, err := strconv.Atoi(strings.TrimSpace(deployment.Replicas))
	if err != nil {
		return "", false, fmt.Errorf("current replicas %q is not an integer", deployment.Replicas)
	}
	if current == target {
		return "", true, nil
	}
	// An empty CPU or memory value would be written through as an empty string
	// and wipe the service's allocation, so the service is left alone instead.
	if deployment.AllocatedCPU == "" || deployment.AllocatedMemory == "" {
		return "", false, fmt.Errorf("current deployment has empty cpu or memory allocation")
	}

	change = fmt.Sprintf("%d->%d", current, target)
	if dryRun {
		return change, false, nil
	}
	if err := patchService(ctx, client, baseURL, cluster, service, deployment, target); err != nil {
		return "", false, err
	}
	fmt.Printf("CALM_PlacementApply: scaled %s/%s replicas %s (cpu %s, memory %s unchanged)\n",
		cluster, service, change, deployment.AllocatedCPU, deployment.AllocatedMemory)
	return change, false, nil
}

// fetchDeployment reads the service's current deployment record.
func fetchDeployment(
	ctx context.Context,
	client *http.Client,
	baseURL, cluster, service string,
) (currentDeployment, error) {
	endpoint := fmt.Sprintf("%s/api/clusters/%s/deployments/%s",
		baseURL, url.PathEscape(cluster), url.PathEscape(service))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return currentDeployment{}, fmt.Errorf("build deployment request failed: %w", err)
	}
	res, err := client.Do(req)
	if err != nil {
		return currentDeployment{}, fmt.Errorf("fetch current deployment failed: %w", err)
	}
	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return currentDeployment{}, fmt.Errorf("read current deployment failed: %w", err)
	}
	if res.StatusCode < http.StatusOK || res.StatusCode >= http.StatusMultipleChoices {
		return currentDeployment{}, fmt.Errorf("fetch current deployment returned %d: %s", res.StatusCode, strings.TrimSpace(string(body)))
	}

	var deployment currentDeployment
	if err := json.Unmarshal(body, &deployment); err != nil {
		return currentDeployment{}, fmt.Errorf("decode current deployment failed: %w", err)
	}
	if deployment.Replicas == "" {
		return currentDeployment{}, fmt.Errorf("current deployment has empty replicas")
	}
	return deployment, nil
}

// patchService writes the target replica count, carrying the current CPU and
// memory allocation through unchanged: the control plane's scaling endpoint
// takes all three together, so omitting them would rewrite them.
func patchService(
	ctx context.Context,
	client *http.Client,
	baseURL, cluster, service string,
	deployment currentDeployment,
	target int,
) error {
	payload := map[string]string{
		"CPU":      deployment.AllocatedCPU,
		"Memory":   deployment.AllocatedMemory,
		"Replicas": strconv.Itoa(target),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal scaling payload failed: %w", err)
	}

	endpoint := fmt.Sprintf("%s/api/clusters/%s/services/%s",
		baseURL, url.PathEscape(cluster), url.PathEscape(service))
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build scaling request failed: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	res, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("scale service failed: %w", err)
	}
	defer res.Body.Close()

	answer, err := io.ReadAll(res.Body)
	if err != nil {
		return fmt.Errorf("read scaling response failed: %w", err)
	}
	if res.StatusCode < http.StatusOK || res.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("scale service returned %d: %s", res.StatusCode, strings.TrimSpace(string(answer)))
	}
	return nil
}

// activeContent returns the payload of the port that actually delivered. A port
// index outside the input slice yields nil, which is reported rather than
// dereferenced: a walker has no recover, so a nil map read here would take the
// whole EFO process down.
func activeContent(input []any, port int) lcm.DataPacketContent {
	if port < 0 || port >= len(input) {
		return nil
	}
	return u.ContentOf(input[port])
}

// decodePlan rebuilds the placement plan from the flattened payload.
//
// The nested calm.KeyReplicaTarget object is the canonical form. When it is
// absent the flattened plan_<service>_replicas keys are read instead, so the
// node works with a policy mode that publishes only the flat view.
func decodePlan(content lcm.DataPacketContent) (calm.PlacementPlan, error) {
	var plan calm.PlacementPlan
	if err := plan.FromMap(content); err != nil {
		return calm.PlacementPlan{}, fmt.Errorf("decode placement plan failed: %w", err)
	}
	if len(plan.ReplicaTarget) > 0 {
		return plan, nil
	}
	targets, err := flatReplicaTargets(content)
	if err != nil {
		return calm.PlacementPlan{}, err
	}
	plan.ReplicaTarget = targets
	return plan, nil
}

// flatReplicaTargets reads the plan_<service>_replicas keys of the payload.
func flatReplicaTargets(content lcm.DataPacketContent) (map[string]int, error) {
	targets := make(map[string]int)
	for _, key := range sortedNames(content) {
		if !strings.HasPrefix(key, inPlanPrefix) || !strings.HasSuffix(key, inPlanReplicasSuffix) {
			continue
		}
		service := strings.TrimSuffix(strings.TrimPrefix(key, inPlanPrefix), inPlanReplicasSuffix)
		if service == "" {
			continue
		}
		target, ok := asInt(content[key])
		if !ok {
			return nil, fmt.Errorf("replica target %q is not an integer: %v", key, content[key])
		}
		targets[service] = target
	}
	return targets, nil
}

// unappliedActions names the plan entries this node cannot carry out. Migration
// and offload have no control-plane counterpart on the testbed, and dropping
// them quietly would make a plan look fully applied when it was not.
func unappliedActions(plan calm.PlacementPlan) []string {
	actions := []string{}
	for index, migration := range plan.Migrations {
		actions = append(actions, fmt.Sprintf("migration[%d] not implemented: %v", index, migration))
	}
	for index, offload := range plan.Offloads {
		actions = append(actions, fmt.Sprintf("offload[%d] not implemented: %v", index, offload))
	}
	return actions
}

// emit encodes the fixed output and adds the dynamic applied_<service> keys.
func emit(out OutputFormat, applied map[string]string) i.NodeExecResult {
	encoded, err := u.EncodeOutput(out, Description)
	if err != nil {
		fmt.Println(Description.NodeType+": Encode output error,", err)
		encoded = u.EmptyOutput(Description.NumOutputPort)
	}
	content := u.ContentOf(encoded[0])
	if content == nil {
		content = lcm.DataPacketContent{}
		encoded[0] = content
	}
	for _, service := range sortedNames(applied) {
		content[AppliedPrefix+service] = applied[service]
	}
	return i.NodeExecResult{Output: encoded, Ports: []int{0}}
}

// sortedNames returns the keys of m in ascending order.
func sortedNames[V any](m map[string]V) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// stringAt reads one payload field as a string, yielding "" for an absent field
// or one holding something else.
func stringAt(content lcm.DataPacketContent, key string) string {
	value, found := content[key]
	if !found {
		return ""
	}
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return text
}

// asInt reads a payload value as an int. A payload that has been through JSON
// carries every number as a float64, and one built in Go carries whichever kind
// the producer used, so both are accepted; a fractional value is rejected
// rather than truncated, since a replica count of 1.5 is a producer bug.
func asInt(value any) (int, bool) {
	switch number := value.(type) {
	case int:
		return number, true
	case int32:
		return int(number), true
	case int64:
		return int(number), true
	case float32:
		return asInt(float64(number))
	case float64:
		if number != math.Trunc(number) {
			return 0, false
		}
		return int(number), true
	case json.Number:
		parsed, err := number.Int64()
		if err != nil {
			return 0, false
		}
		return int(parsed), true
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(number))
		return parsed, err == nil
	default:
		return 0, false
	}
}
