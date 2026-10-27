/*
Package interfaces holds the LCM_Enabler design/management data model: service,
policy, module and template configuration, the policy graph building blocks, the
data carried between nodes, and the LCM API DTOs.

This package imports nothing (leaf). The node-execution ABI (IPolicyNode, ExecInfo,
NodeExecResult, FunctionNode, TriggerNode, NodeDescription, TriggerDescription,
NumPort) lives in Rule_Framework/interfaces, which imports this package.
*/
package interfaces

/*
 * Node data model (the data a policy graph carries)
 */

type NodeOutput []DataPacketContent

type DataPacketContent map[string]any

type NodeConfig struct {
	NodeName string `json:"name" yaml:"name"`
	NodeType string `json:"type" yaml:"type"`
	Property any    `json:"property" yaml:"property"`
	// FillPropertyFromInput lets the first input DataPacket fill property
	// fields that the static config left empty; see nodes/utils.
	FillPropertyFromInput bool `json:"fill_property_from_input,omitempty" yaml:"fill_property_from_input,omitempty"`
}

/*
 * Module
 */

type ModuleConfig struct {
	ModuleName string             `json:"name" yaml:"name"`
	NumInPort  int                `json:"numInPort" yaml:"numInPort"`
	NumOutPort int                `json:"numOutPort" yaml:"numOutPort"`
	InPort     []ModulePortConfig `json:"inPort" yaml:"inPort"`
	OutPort    []ModulePortConfig `json:"outPort" yaml:"outPort"`
	ModuleNode []NodeConfig       `json:"node" yaml:"node"`
	ModulePath []ConnectionUnit   `json:"path" yaml:"path"`
	// UI is optional and GUI-owned; see CanvasLayout.
	UI *CanvasLayout `json:"ui,omitempty" yaml:"ui,omitempty"`
}

type ModulePortConfig struct {
	ModulePort int    `json:"modulePort" yaml:"modulePort"`
	NodeName   string `json:"nodeName" yaml:"nodeName"`
	NodePort   int    `json:"nodePort" yaml:"nodePort"`
}

/*
 * Canvas layout
 */

// CanvasPoint is where a node sits on the design GUI's canvas.
type CanvasPoint struct {
	X float64 `json:"x" yaml:"x"`
	Y float64 `json:"y" yaml:"y"`
}

// CanvasLayout is design-time GUI state stored beside a policy or a module.
//
// It exists so that a graph someone arranged by hand survives a save and a
// reload instead of snapping back to the automatic grid. The engine never reads
// it and no policy behavior may depend on it, which is why it sits on the
// policy rather than on each node: one clearly named block, quarantined from
// the node model the engine does read.
//
// Node is keyed by canvas node id -- a node's own name, and "__in:N" / "__out:N"
// for a module's own ports, which are drawn as nodes but stored as an arity. A
// key naming a node that no longer exists is ignored, and a node with no key is
// placed automatically, so a hand-edited config never has to keep this block in
// step. Deleting the whole block restores the automatic layout.
//
// Both struct tags are mandatory. TransFormat below records what happens when
// only one direction is tagged.
type CanvasLayout struct {
	Node map[string]CanvasPoint `json:"node,omitempty" yaml:"node,omitempty"`
}

/*
 * Service / Policy / Template
 */

type ServiceConfig struct {
	SvcName string         `json:"service_name" yaml:"service_name"`
	Module  []ModuleConfig `json:"module" yaml:"module"`
	Policy  []PolicyConfig `json:"policy" yaml:"policy"`
}

type PolicyConfig struct {
	PolicyName    string           `json:"name" yaml:"name"`
	PolicyTrigger []NodeConfig     `json:"trigger" yaml:"trigger"`
	PolicyNode    []NodeConfig     `json:"node" yaml:"node"`
	PolicyPath    []ConnectionUnit `json:"path" yaml:"path"`
	// UI is optional and GUI-owned; see CanvasLayout. A pointer with omitempty
	// so a config that does not set it serializes exactly as it did before the
	// field existed, which is what keeps hand-written YAML working untouched.
	UI *CanvasLayout `json:"ui,omitempty" yaml:"ui,omitempty"`
}

type Template struct {
	TemplateName string         `json:"template_name" yaml:"template_name"`
	SvcName      string         `json:"service_name" yaml:"service_name"`
	Policy       []PolicyConfig `json:"policy" yaml:"policy"`
}

type ConnectionUnit struct {
	NodePair    [2]string   `json:"nodePair" yaml:"nodePair"`
	PortPair    [2]int      `json:"portPair" yaml:"portPair"`
	TransFormat TransFormat `json:"transFormat" yaml:"transFormat"`
	// Carry names the upstream namespaces this connection forwards, in the
	// order that decides which one wins a field-name clash. Empty forwards
	// everything, matching how an empty TransFormat means pass-through.
	Carry []string `json:"carry" yaml:"carry"`
}

// TransFormat renames fields as they cross one connection. An empty list is
// pass-through.
//
// The tags are load-bearing in one direction only, which is what made their
// absence so easy to miss: encoding/json matches incoming keys case
// insensitively, so a browser posting {"src","dst"} always decoded correctly,
// but marshalling emitted the Go field names and sent {"Src","Dst"} back. The
// GUI then read undefined for every mapping it loaded.
type TransFormat []struct {
	Src string `json:"src" yaml:"src"`
	Dst string `json:"dst" yaml:"dst"`
}

/*
 * LCM API DTOs
 */

type WebhookEvent struct {
	SvcName   string `json:"SvcName"`
	EventType string `json:"EventType"`
	Content   any    `json:"Content"`
}

type DeployPolicy struct {
	SvcName  string   `json:"SvcName"`
	Policies []string `json:"Policies"`
}

type TemplateConfigure struct {
	SvcName      string `json:"SvcName"`
	TemplateName string `json:"TemplateName"`
}

type PolicyInfo struct {
	Name   string
	Status string
	Config any
}

/*
 * Data packet info (returned by the runtime; re-serialized by the design layer)
 */

type DataPacketInfo struct {
	Name  string
	Input struct {
		InPort  int
		Content DataPacketContent
	}
	Output struct {
		OutPort int
		Content DataPacketContent
	}
	TransFormat TransFormat
}

/*
 * Execution trace (produced by the runtime; read by the design GUI)
 *
 * A run is one trigger event walking the policy DAG. Only the latest run of a
 * policy is kept, so these types describe a snapshot, not a history.
 */

// TriggerInfo identifies the trigger that started a run and the event payload
// it carried.
type TriggerInfo struct {
	Node     string            `json:"node"`
	NodeType string            `json:"nodeType"`
	Content  DataPacketContent `json:"content"`
}

// WaitingNode is a node whose walker is parked waiting for an input port that
// has not been produced. Within a finished run these are the branches that will
// never proceed.
type WaitingNode struct {
	Node  string `json:"node"`
	Scope string `json:"scope"`
	Port  int    `json:"port"`
}

// NodeEvent is one node execution within a run.
//
// Input, InputRaw and InputErrors are parallel arrays indexed by input port.
// All three are needed because the value a node receives is the post-transFormat
// content, which equals the upstream payload when no transFormat is configured
// but is a projection of it otherwise; InputRaw is the only view of the
// pre-transform payload, and InputErrors surfaces transform failures that the
// engine otherwise only writes to the log.
type NodeEvent struct {
	Seq           int                 `json:"seq"`
	Node          string              `json:"node"`
	NodeType      string              `json:"nodeType"`
	Scope         string              `json:"scope"`
	InPort        int                 `json:"inPort"`
	StartOffsetMs int64               `json:"startOffsetMs"`
	DurationMs    int64               `json:"durationMs"`
	Input         []any               `json:"input"`
	InputRaw      []DataPacketContent `json:"inputRaw"`
	InputErrors   []string            `json:"inputErrors"`
	Output        NodeOutput          `json:"output"`
	Ports         []int               `json:"ports"`
	Status        string              `json:"status"`
}

// RunTrace is the snapshot returned by the run endpoint.
//
// Status is "no-run" before the first trigger, "running" while at least one
// walker can still execute, and "complete" once no further event is possible.
// Events is filtered to Seq > Since; EventCount is always the untruncated total
// so a client can detect a missed window.
type RunTrace struct {
	Policy     string        `json:"policy"`
	HasRun     bool          `json:"hasRun"`
	RunID      string        `json:"runId"`
	Trigger    TriggerInfo   `json:"trigger"`
	StartedAt  string        `json:"startedAt"`
	UpdatedAt  string        `json:"updatedAt"`
	DurationMs int64         `json:"durationMs"`
	Status     string        `json:"status"`
	Active     int           `json:"active"`
	Blocked    int           `json:"blocked"`
	Waiting    []WaitingNode `json:"waiting"`
	EventCount int           `json:"eventCount"`
	Since      int           `json:"since"`
	Truncated  bool          `json:"truncated"`
	Events     []NodeEvent   `json:"events"`
}

/*
 * Node schema (design-time introspection)
 *
 * Field names are the RUNTIME MAP KEYS, verbatim. For Property they come from
 * the yaml tag because GetProperty decodes with TagName "yaml"; for Input and
 * Output they are the Go field names because DecodeInput/EncodeOutput use
 * mapstructure's default tag, which no node sets.
 *
 * Names are never dotted. transformContent can only address a top-level key, so
 * a dotted Src would silently resolve to the wrong value.
 */

// FieldSchema describes one field a design GUI can render or map.
type FieldSchema struct {
	Name string `json:"name"`
	// Type is one of: string, integer, number, boolean, string[], array,
	// object, object[], unknown.
	Type     string   `json:"type"`
	Enum     []string `json:"enum,omitempty"`
	Required bool     `json:"required,omitempty"`
	Secret   bool     `json:"secret,omitempty"`
	FilePath bool     `json:"filePath,omitempty"`
	// FieldRef marks a property whose value names a field of the incoming
	// payload rather than being a value itself, such as the field an If
	// condition reads. A GUI can then offer what actually arrives at that node
	// instead of leaving the user to type a name from memory.
	FieldRef bool `json:"fieldRef,omitempty"`
	// FieldRefPort is the input port the reference reads, meaningful only when
	// FieldRef is set. Compare's two sides read one port each; If's single
	// field reads port 0.
	FieldRefPort int    `json:"fieldRefPort,omitempty"`
	Doc          string `json:"doc,omitempty"`
	// ShowWhen lists the discriminator values that use this property. A property
	// is both shown and required exactly when the current value is in this list.
	ShowWhen []string `json:"showWhen,omitempty"`
	// Item describes one element's fields when Type is "object[]", so a GUI can
	// render a repeatable row editor. Properties only, and one level deep: port
	// fields are never expanded because transformContent addresses top-level
	// keys alone.
	Item []FieldSchema `json:"item,omitempty"`
}

// PortSchema is the field set carried on a node's input or output port.
type PortSchema struct {
	Fields []FieldSchema `json:"fields"`
	// Dynamic marks a shape decided at runtime, such as a webhook payload, so
	// the GUI offers free text instead of a closed list.
	Dynamic bool `json:"dynamic,omitempty"`
}

// NodeSchema is everything a design GUI needs to render and validate one node
// type without hard-coding its fields.
type NodeSchema struct {
	Type          string   `json:"type"`
	Kind          string   `json:"kind"` // "trigger" | "function"
	NumInputPort  int      `json:"numInputPort"`
	NumOutputPort int      `json:"numOutputPort"`
	OutputLabels  []string `json:"outputLabels,omitempty"`
	// Discriminator names the property whose value gates ShowWhen, e.g. "request".
	Discriminator string        `json:"discriminator,omitempty"`
	Properties    []FieldSchema `json:"properties"`
	// Input is nil when the node declares no input format at all and reads only
	// its properties; non-nil with empty Fields when it declares an empty one.
	Input  *PortSchema `json:"input"`
	Output *PortSchema `json:"output"`
	// Passthrough marks a node that contributes no namespace of its own: what
	// leaves it is exactly what arrived. The edge editor needs this to avoid
	// offering the node as a namespace to carry, which would look like a way to
	// forward its payload while actually forwarding nothing.
	Passthrough bool `json:"passthrough,omitempty"`
}
