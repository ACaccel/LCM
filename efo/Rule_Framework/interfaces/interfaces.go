/*
Package interfaces holds the Rule_Framework runtime contracts: the node-execution
ABI driven by the policy engine, and IRule_framework — the seam through which the
LCM_Enabler (design/management) delegates runtime policy execution.

It depends only on the LCM_Enabler interfaces (the policy design model), keeping the
dependency direction acyclic: design model (leaf) <- runtime ABI <- engine.
*/
package interfaces

import lcm "1935/efo/LCM_Enabler/interfaces"

/*
 * Node execution ABI (driven by the policy engine)
 */

// IPolicyNode is implemented by every trigger and function node.
type IPolicyNode interface {
	Execute(input []any, info ExecInfo) NodeExecResult
}

// ExecInfo carries the per-execution context passed to a node.
type ExecInfo struct {
	Service_Name string
	File_Path    string
	ActiveInPort int
	Content      any
	WebhookCh    chan any
	// TriggerCh is shared by every trigger in the policy, so an event names the
	// trigger that produced it. Without that the policy could not tell whose
	// successors to walk, and an event would be handled from whichever trigger
	// happened to be reading.
	TriggerCh chan TriggerEvent
	// StopCh is closed when the policy is undeployed. Closing broadcasts to
	// every goroutine at once and never blocks, which a send cannot do: the
	// per-trigger sends this replaced deadlocked whenever a trigger was already
	// parked handing an event over.
	StopCh <-chan struct{}
	// ControlPlaneBaseURL is the base URL action nodes use to reach the EFO
	// control plane (default http://localhost:8000). Injected at deploy time.
	ControlPlaneBaseURL string
	// RunID identifies the execution this node call belongs to. It is minted
	// when a trigger event is received and threaded down the DAG so trace
	// events can be correlated; nodes may use it to tag their own logs.
	RunID string
	// EntryNamespace names what the payload in Content should be filed under,
	// for a node entered at a boundary rather than along an edge. A module
	// entry is the only such node: it has no inbound packet, so there are no
	// upstream namespaces to carry, and without a name of their own the values
	// arriving at the boundary would survive only if the entry node happened to
	// re-emit them.
	EntryNamespace string
	// RunDone is closed when the run this call belongs to is over: superseded
	// by a newer trigger event, or ended by undeploy. It exists because the
	// engine resets every packet between runs, and a walker still touching one
	// while that happens reads a half-cleared packet or sends on a channel the
	// reset closed. Closing this first, and waiting for the walkers to leave,
	// is what makes the reset safe.
	//
	// A node that blocks for a noticeable time should select on it, so that
	// undeploying does not have to wait out the delay.
	RunDone <-chan struct{}
	// Namespaces is what arrived on each input port, still grouped by the node
	// that produced it. Content is the flattened view of the same data, and is
	// what a node normally reads; this is the escape hatch for naming a field
	// whose owner matters, such as "webhook-0.Value" when a later node also
	// emitted Value. Indexed by input port, and empty for a trigger or a module
	// entry, which receive a flat payload with no namespaces to speak of.
	Namespaces []lcm.DataPacketContent
}

// TriggerEvent is one trigger firing: what it produced, and which trigger it
// was.
type TriggerEvent struct {
	Node    string
	Content lcm.NodeOutput
}

// NodeExecResult is returned by a node after execution.
type NodeExecResult struct {
	Output lcm.NodeOutput
	Ports  []int
}

// FunctionNode is the base embedded by every function node implementation.
type FunctionNode struct {
	Description NodeDescription
	Config      lcm.NodeConfig
}

// TriggerNode is the base embedded by every trigger node implementation.
type TriggerNode struct {
	Description TriggerDescription
	Config      lcm.NodeConfig
}

// JoinMode decides when a node with several inputs is allowed to run.
type JoinMode int

const (
	// JoinAll waits for every connected input port before executing. It is the
	// zero value, so a node that says nothing keeps the historical behavior.
	JoinAll JoinMode = iota
	// JoinAny executes as soon as the first input arrives and skips the rest.
	//
	// This is what lets two branches rejoin. Only one side of an If ever runs,
	// so a JoinAll node placed after the split waits forever on an input no
	// walker will ever produce.
	JoinAny
)

// JoinModeDecider is implemented by a node whose join mode is a setting rather
// than a property of its type. The engine asks the constructed node, which
// already holds its Config, so the answer can depend on how this instance was
// configured.
type JoinModeDecider interface {
	JoinMode() JoinMode
}

// NodeDescription describes a function node's type and port arity.
type NodeDescription struct {
	NodeType      string
	NumInputPort  int
	NumOutputPort int
	// Join decides how the engine treats several inputs. Leave it unset for
	// the wait-for-everything default. A node that implements JoinModeDecider
	// overrides this per instance.
	Join JoinMode
}

// TriggerDescription describes a trigger node's type.
type TriggerDescription struct {
	TriggerType string
}

// NumPort holds a node's input/output port counts.
type NumPort struct {
	NumInput  int
	NumOutput int
}

/*
 * IRule_framework: the runtime engine the LCM_Enabler delegates to.
 *
 * Rule framework is responsible for dealing with all conditions, requirements,
 * constraints, attributes, and reaction policies that must be provided,
 * maintained, and/or enforced at runtime.
 */
type IRule_framework interface {
	// Deploy instantiates and starts a policy from its design config.
	Deploy(svcName string, cfg lcm.PolicyConfig, modules map[string]lcm.ModuleConfig) error
	// Undeploy stops and removes a deployed policy.
	Undeploy(svcName, policyName string) error
	// IsDeployed reports whether a policy is currently running.
	IsDeployed(svcName, policyName string) bool
	// DeployedPolicies lists the names of a service's running policies.
	DeployedPolicies(svcName string) []string
	// ExecuteWebhook dispatches an event to every running webhook trigger
	// subscribed to eventType in the service, returning the number dispatched.
	ExecuteWebhook(svcName, eventType string, content any) (dispatched int, err error)
	// LatestRun returns the latest execution trace of a running policy, keeping
	// only events newer than since.
	LatestRun(svcName, policyName string, since int) (lcm.RunTrace, error)
	// TerminateService undeploys all running policies of a service.
	TerminateService(svcName string) error
	// ValidateModule reports whether a module could be deployed, without
	// deploying it. The design layer needs the same answer the engine will give
	// so a broken module is refused at save time rather than at deploy time.
	ValidateModule(moduleName string, modules map[string]lcm.ModuleConfig) error
	// ModuleReferences maps each Module node in a policy to the module it
	// instantiates. Which module a node means is the engine's rule, so the
	// design layer asks rather than reimplementing it.
	ModuleReferences(cfg lcm.PolicyConfig) map[string]string
}
