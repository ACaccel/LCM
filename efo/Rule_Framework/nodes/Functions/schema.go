package Functions

import (
	"net/http"

	lcm "1935/efo/LCM_Enabler/interfaces"
	"1935/efo/Rule_Framework/nodes/Functions/calm"
	"1935/efo/Rule_Framework/nodes/schema"

	"1935/efo/Rule_Framework/nodes/Functions/CALM_PlacementApply"
	"1935/efo/Rule_Framework/nodes/Functions/CALM_PolicyMode"
	"1935/efo/Rule_Framework/nodes/Functions/CALM_PolicySelector"
	"1935/efo/Rule_Framework/nodes/Functions/CALM_PostVerify"
	"1935/efo/Rule_Framework/nodes/Functions/CALM_Telemetry"
	"1935/efo/Rule_Framework/nodes/Functions/IEEE1935_AppStore"
	"1935/efo/Rule_Framework/nodes/Functions/IEEE1935_Application"
	"1935/efo/Rule_Framework/nodes/Functions/IEEE1935_Cluster"
	"1935/efo/Rule_Framework/nodes/Functions/IEEE1935_Deployment"
	"1935/efo/Rule_Framework/nodes/Functions/IEEE1935_Image"
	"1935/efo/Rule_Framework/nodes/Functions/IEEE1935_Jaeger"
	"1935/efo/Rule_Framework/nodes/Functions/IEEE1935_Machine"
	"1935/efo/Rule_Framework/nodes/Functions/IEEE1935_Manifest"
	"1935/efo/Rule_Framework/nodes/Functions/IEEE1935_Node"
	"1935/efo/Rule_Framework/nodes/Functions/IEEE1935_Resourceoptimizer"
	"1935/efo/Rule_Framework/nodes/Functions/IEEE1935_Service"
	"1935/efo/Rule_Framework/nodes/Functions/HTTPRequest"
	"1935/efo/Rule_Framework/nodes/Functions/If"
	"1935/efo/Rule_Framework/nodes/Functions/Compare"
	"1935/efo/Rule_Framework/nodes/Functions/MachinePropertySource"
	"1935/efo/Rule_Framework/nodes/Functions/Merge"
	"1935/efo/Rule_Framework/nodes/Functions/Request_tracer"
	"1935/efo/Rule_Framework/nodes/Functions/Wait"
)

/*
Design-time schema for every function node.

Schema is the third map parallel to Description and Function. Adding a node
means one line in each: the field names, types and port arity come from
reflection over the node package's own types, so only what reflection cannot
recover is written by hand below.
*/

var Schema = make(map[string]lcm.NodeSchema)

// registrations names the types reflection cannot reach from a constructed node
// value, since every node embeds only FunctionNode{Description, Config}.
//
//	In: nil -- the node declares no input format; it reads only its properties.
//	In: T{} -- the node declares input format T, even when T is empty.
var registrations = []schema.Registration{
	// These four decide routing or timing and emit nothing of their own, so
	// they are marked Passthrough: what arrives travels on untouched as the
	// namespaces it came with. Their ports read any key a condition names and
	// hold no fixed field set, hence dynamic rather than reflected.
	// Merge's mode chooses its join behavior, so one type covers both.
	{Type: "If", Prop: If.Property{}, Passthrough: true,
		InPort:  &lcm.PortSchema{Fields: []lcm.FieldSchema{}, Dynamic: true},
		OutPort: &lcm.PortSchema{Fields: []lcm.FieldSchema{}, Dynamic: true}},
	{Type: "Compare", Prop: Compare.Property{}, Passthrough: true,
		InPort:  &lcm.PortSchema{Fields: []lcm.FieldSchema{}, Dynamic: true},
		OutPort: &lcm.PortSchema{Fields: []lcm.FieldSchema{}, Dynamic: true}},
	{Type: "Merge", Prop: Merge.Property{}, Passthrough: true,
		InPort:  &lcm.PortSchema{Fields: []lcm.FieldSchema{}, Dynamic: true},
		OutPort: &lcm.PortSchema{Fields: []lcm.FieldSchema{}, Dynamic: true}},
	{Type: "Wait", Prop: Wait.Property{}, Passthrough: true,
		InPort:  &lcm.PortSchema{Fields: []lcm.FieldSchema{}, Dynamic: true},
		OutPort: &lcm.PortSchema{Fields: []lcm.FieldSchema{}, Dynamic: true}},
	{Type: "HTTPRequest", In: nil, Out: HTTPRequest.OutputFormat{}, Prop: HTTPRequest.Property{}},

	{Type: "IEEE1935_AppStore", In: nil, Out: IEEE1935_AppStore.OutputFormat{}, Prop: IEEE1935_AppStore.Property{}},
	{Type: "IEEE1935_Application", In: nil, Out: IEEE1935_Application.OutputFormat{}, Prop: IEEE1935_Application.Property{}},
	{Type: "IEEE1935_Cluster", In: nil, Out: IEEE1935_Cluster.OutputFormat{}, Prop: IEEE1935_Cluster.Property{}},
	{Type: "IEEE1935_Deployment", In: nil, Out: IEEE1935_Deployment.OutputFormat{}, Prop: IEEE1935_Deployment.Property{}},
	{Type: "IEEE1935_Image", In: nil, Out: IEEE1935_Image.OutputFormat{}, Prop: IEEE1935_Image.Property{}},
	{Type: "IEEE1935_Jaeger", In: nil, Out: IEEE1935_Jaeger.OutputFormat{}, Prop: IEEE1935_Jaeger.Property{}},
	{Type: "IEEE1935_Machine", In: nil, Out: IEEE1935_Machine.OutputFormat{}, Prop: IEEE1935_Machine.Property{}},
	{Type: "IEEE1935_Manifest", In: nil, Out: IEEE1935_Manifest.OutputFormat{}, Prop: IEEE1935_Manifest.Property{}},
	{Type: "IEEE1935_Node", In: nil, Out: IEEE1935_Node.OutputFormat{}, Prop: IEEE1935_Node.Property{}},
	{Type: "IEEE1935_Resourceoptimizer", In: nil, Out: IEEE1935_Resourceoptimizer.OutputFormat{}, Prop: IEEE1935_Resourceoptimizer.Property{}},
	// The only node that declares an input format and never reads it.
	{Type: "IEEE1935_Service", In: IEEE1935_Service.InputFormat{}, Out: IEEE1935_Service.OutputFormat{}, Prop: IEEE1935_Service.Property{}},
	{Type: "Request_tracer", In: nil, Out: Request_tracer.OutputFormat{}, Prop: Request_tracer.Property{}},
	// The output is a hand-built map keyed by IEEE1935_Machine's yaml tags, not
	// a struct reflection can reach, so the port fields are written out here.
	{Type: "MachinePropertySource", In: nil, Prop: MachinePropertySource.Property{},
		OutPort: &lcm.PortSchema{Fields: []lcm.FieldSchema{
			{Name: "request", Type: "string"},
			{Name: "machine_Name", Type: "string"},
			{Name: "login_name", Type: "string"},
			{Name: "login_passwd", Type: "string"},
			{Name: "network_ip", Type: "string"},
			{Name: "network_port", Type: "string"},
			{Name: "area_name", Type: "string"},
		}}},

	// CALM nodes read the flat payload of the whole pipeline rather than a
	// declared input struct, so their in-ports are dynamic. Outputs that carry
	// per-service keys (svc_<name>_*, plan_<name>_*, applied_<name>) on top of
	// the fixed struct are marked dynamic for the same reason.
	{Type: "CALM_Telemetry", In: nil, Prop: CALM_Telemetry.Property{},
		OutPort: &lcm.PortSchema{
			Fields:  append(schema.Fields(CALM_Telemetry.OutputFormat{}, schema.PortTag), calmStateFields...),
			Dynamic: true}},
	{Type: "CALM_PolicySelector", Out: CALM_PolicySelector.OutputFormat{}, Prop: CALM_PolicySelector.Property{},
		InPort: &lcm.PortSchema{Fields: []lcm.FieldSchema{}, Dynamic: true}},
	{Type: "CALM_PolicyMode", Prop: CALM_PolicyMode.Property{},
		InPort: &lcm.PortSchema{Fields: []lcm.FieldSchema{}, Dynamic: true},
		OutPort: &lcm.PortSchema{
			Fields:  schema.Fields(CALM_PolicyMode.OutputFormat{}, schema.PortTag),
			Dynamic: true}},
	{Type: "CALM_PlacementApply", Prop: CALM_PlacementApply.Property{},
		InPort: &lcm.PortSchema{Fields: []lcm.FieldSchema{}, Dynamic: true},
		OutPort: &lcm.PortSchema{
			Fields:  schema.Fields(CALM_PlacementApply.OutputFormat{}, schema.PortTag),
			Dynamic: true}},
	{Type: "CALM_PostVerify", Prop: CALM_PostVerify.Property{},
		InPort: &lcm.PortSchema{Fields: []lcm.FieldSchema{}, Dynamic: true},
		OutPort: &lcm.PortSchema{
			Fields:  schema.Fields(CALM_PostVerify.OutputFormat{}, schema.PortTag),
			Dynamic: true}},
}

// calmStateFields are the StateVector keys CALM_Telemetry always emits via
// ToMap. They are listed by the calm package's own key constants so the GUI's
// field pickers can offer them, and cannot drift from the wire.
var calmStateFields = []lcm.FieldSchema{
	{Name: calm.KeyMeanSinr, Type: "number"},
	{Name: calm.KeyMeanPrbUtil, Type: "number"},
	{Name: calm.KeyHoRate, Type: "number"},
	{Name: calm.KeyMeanIntf, Type: "number"},
	{Name: calm.KeyRadioMasked, Type: "boolean"},
	{Name: calm.KeyMeanCpu, Type: "number"},
	{Name: calm.KeyMaxCpu, Type: "number"},
	{Name: calm.KeyMeanMem, Type: "number"},
	{Name: calm.KeyMeanGpu, Type: "number"},
	{Name: calm.KeyTotalReqRate, Type: "number"},
	{Name: calm.KeyMeanD99Ms, Type: "number"},
	{Name: calm.KeyMaxDmr, Type: "number"},
	{Name: calm.KeyLatencySloMs, Type: "number"},
	{Name: calm.KeyReplicaTotal, Type: "integer"},
}

// calmModeNames is the canonical mode order; port index == arm index on the
// selector, so the labels must not be reordered.
var calmModeNames = []string{
	string(calm.ModeLatencyPriority),
	string(calm.ModeBalanced),
	string(calm.ModeEnergySaving),
	string(calm.ModeResilient),
}

// comparisons are the operators If and Compare accept. An unrecognised value logs
// and leaves the comparison false, which routes to the false port and looks
// exactly like a failed comparison -- hence the closed list.
var comparisons = []string{"==", "!=", ">", "<", ">=", "<="}

// mergeModes are the values Merge accepts for mode.
var mergeModes = []string{Merge.ModeAll, Merge.ModeAny}

// httpMethods are the verbs offered for a generic HTTP call. Not a runtime
// restriction -- net/http accepts any token -- but a closed list in the GUI.
var httpMethods = []string{
	http.MethodGet, http.MethodPost, http.MethodPut,
	http.MethodPatch, http.MethodDelete, http.MethodHead,
}

// nodeMeta keys are PROPERTY WIRE KEYS: the yaml tag when present, the Go field
// name otherwise. TestSchema_MetaKeysMatchRealFields enforces that.
//
// ShowWhen lists the request values that use a property, and doubles as the
// required-for set: every property a request reads is one that request needs.
// An unrecognised request falls through to a 400 with no other signal, so the
// closed lists below are a correctness feature, not a convenience.
var nodeMeta = map[string]schema.Meta{
	"If": {
		Enums:        map[string][]string{"combinator": {If.CombinatorAnd, If.CombinatorOr}},
		Required:     []string{"conditions", "combinator"},
		OutputLabels: []string{"true (0)", "false (1)"},
		Docs: map[string]string{
			"conditions": "each condition compares one payload field against a number",
			"combinator": "joins every condition; it applies to the whole list, so mixed AND/OR needs a second If",
		},
		ItemMeta: map[string]schema.Meta{
			"conditions": {
				Enums:    map[string][]string{"operator": comparisons},
				Required: []string{"field", "operator"},
				FieldRef: []string{"field"},
				Docs: map[string]string{
					"field": "key to read from the incoming payload; qualify it as node.field when two upstream nodes emit the same name",
				},
			},
		},
	},
	"Compare": {
		Enums:        map[string][]string{"combinator": {Compare.CombinatorAnd, Compare.CombinatorOr}},
		Required:     []string{"conditions", "combinator"},
		OutputLabels: []string{"true (0)", "false (1)"},
		Docs: map[string]string{
			"conditions": "each condition compares a field of input 0 against a field of input 1",
			"combinator": "joins every condition; it applies to the whole list, so mixed AND/OR needs a second Compare",
		},
		ItemMeta: map[string]schema.Meta{
			"conditions": {
				Enums:    map[string][]string{"operator": comparisons},
				Required: []string{"left", "operator", "right"},
				FieldRef: []string{"left", "right"},
				Docs: map[string]string{
					"left":  "key to read from the payload on input port 0; qualify it as node.field when two upstream nodes emit the same name",
					"right": "key to read from the payload on input port 1; same qualification applies",
				},
			},
		},
	},
	"Merge": {
		Enums:    map[string][]string{"mode": mergeModes},
		Required: []string{"mode"},
		Docs: map[string]string{
			"mode": "all waits for both inputs; any continues as soon as either arrives, which is what an If's two branches need to rejoin",
		},
	},
	"Wait": {
		Required: []string{"seconds"},
		Docs: map[string]string{
			"seconds": "how long to hold this branch; capped by LCM_MAX_WAIT_SECONDS",
		},
	},
	"HTTPRequest": {
		Enums:    map[string][]string{"method": httpMethods},
		Required: []string{"method", "url"},
		Docs: map[string]string{
			"url":          "full URL including scheme; this node does not use ControlPlaneBaseURL",
			"body":         "request body, sent verbatim",
			"content_type": "Content-Type header; set it when sending a body",
			"headers":      "additional request headers",
		},
		ItemMeta: map[string]schema.Meta{
			"headers": {Required: []string{"name"}},
		},
	},

	"IEEE1935_AppStore": {
		Discriminator: "request",
		Enums:         map[string][]string{"request": {"Get_AppStore", "Create_AppStore", "Remove_AppStore"}},
		ShowWhen: map[string][]string{
			"login_name":       {"Create_AppStore"},
			"login_passwd":     {"Create_AppStore"},
			"network_ip":       {"Create_AppStore"},
			"network_port":     {"Create_AppStore"},
			"on_which_machine": {"Create_AppStore"},
		},
		Secret: []string{"login_passwd"},
	},
	"IEEE1935_Application": {
		Discriminator: "request",
		Enums:         map[string][]string{"request": {"List_App", "Get_App", "Design_App", "Update_App", "Remove_App"}},
		ShowWhen: map[string][]string{
			"app_name":        {"Get_App", "Design_App", "Update_App", "Remove_App"},
			"manifest":        {"Design_App", "Update_App"},
			"manifest_number": {"Design_App", "Update_App"},
			"yaml_file":       {"Design_App", "Update_App"},
		},
		FilePath: []string{"yaml_file"},
	},
	"IEEE1935_Cluster": {
		Discriminator: "request",
		Enums:         map[string][]string{"request": {"List_Cluster", "Get_Cluster", "Create_Cluster", "Get_Cluster_join_cmd", "Delete_Cluster"}},
		ShowWhen: map[string][]string{
			"cluster_Name": {"Get_Cluster", "Create_Cluster", "Get_Cluster_join_cmd", "Delete_Cluster"},
			"machine_Name": {"Create_Cluster"},
			"node_Name":    {"Create_Cluster"},
			"ovaPath":      {"Create_Cluster"},
		},
	},
	"IEEE1935_Deployment": {
		Discriminator: "request",
		Enums:         map[string][]string{"request": {"List_Deployment", "Get_Deployment", "Deploy_Deployment", "Update_Deployment", "Remove_Deployment"}},
		ShowWhen: map[string][]string{
			"allocated_cpu":    {"Deploy_Deployment", "Update_Deployment"},
			"allocated_memory": {"Deploy_Deployment", "Update_Deployment"},
			"app_name":         {"Get_Deployment", "Deploy_Deployment", "Update_Deployment", "Remove_Deployment"},
			"cluster_name":     {"List_Deployment", "Get_Deployment", "Deploy_Deployment", "Update_Deployment", "Remove_Deployment"},
			"replicas":         {"Deploy_Deployment", "Update_Deployment"},
		},
	},
	"IEEE1935_Image": {
		Discriminator: "request",
		Enums:         map[string][]string{"request": {"List_Image", "List_Image_InCluster", "Build_Image", "Remove_Image"}},
		ShowWhen: map[string][]string{
			"cluster_Name":  {"List_Image_InCluster"},
			"image_name":    {"Remove_Image"},
			"manifest_name": {"Build_Image"},
		},
	},
	"IEEE1935_Jaeger": {
		// Request is a URL path suffix appended to URL, not a closed set.
		Required: []string{"url", "request"},
		Docs: map[string]string{
			"url":     "Jaeger base URL; this node does not use ControlPlaneBaseURL",
			"request": "API path appended to URL, e.g. api/services",
		},
	},
	"IEEE1935_Machine": {
		Discriminator: "request",
		Enums:         map[string][]string{"request": {"List_Machine", "Get_Machine", "Create_VM", "Join_Machine", "Delete_Machine"}},
		ShowWhen: map[string][]string{
			"area_name":        {"Join_Machine"},
			"cpu":              {"Create_VM"},
			"login_name":       {"Join_Machine"},
			"login_passwd":     {"Join_Machine"},
			"machine_Name":     {"Get_Machine", "Join_Machine", "Delete_Machine"},
			"memory":           {"Create_VM"},
			"name":             {"Create_VM"},
			"network_ip":       {"Join_Machine"},
			"network_port":     {"Join_Machine"},
			"on_which_machine": {"Create_VM"},
			"ovaPath":          {"Create_VM"},
		},
		Secret: []string{"login_passwd"},
	},
	"IEEE1935_Manifest": {
		Discriminator: "request",
		Enums:         map[string][]string{"request": {"List_Manifest", "Get_Manifest", "Onboarded_Manifest", "Update_Manifest", "Remove_Manifest"}},
		ShowWhen: map[string][]string{
			"manifest_file": {"Get_Manifest", "Onboarded_Manifest", "Update_Manifest", "Remove_Manifest"},
		},
		FilePath: []string{"manifest_file"},
	},
	"IEEE1935_Node": {
		Discriminator: "request",
		Enums:         map[string][]string{"request": {"List_Node", "Get_Node", "Join_Node", "Delete_Node"}},
		ShowWhen: map[string][]string{
			"cluster_Name": {"List_Node", "Get_Node", "Join_Node", "Delete_Node"},
			"machine_Name": {"Join_Node"},
			"node_Name":    {"Get_Node", "Join_Node", "Delete_Node"},
		},
	},
	"IEEE1935_Resourceoptimizer": {
		Required: []string{"ip", "port"},
		Docs: map[string]string{
			"ip":              "host of the external ResourceAllocationApp (PSO optimizer)",
			"port":            "port of the external ResourceAllocationApp",
			"collectdelay":    "also collect per-request arrival times and latencies from Jaeger, not just request counts",
			"alloctionfactor": "fraction of each cluster's collected CPU/Memory offered to the optimizer; unset or <= 0 means 1.0",
		},
	},
	"CALM_Telemetry": {
		Required: []string{"target_cluster"},
		FilePath: []string{"inject_file"},
		Docs: map[string]string{
			"target_cluster":        "the one cluster this CALM loop observes and scales; the decision domain",
			"sample_window_seconds": "how far back the Jaeger scan reaches; the denominator of every request rate",
			"jaeger_limit":          "max trace pages read per service; hitting it sets sampling_truncated and rates become a lower bound",
			"inject_file":           "optional JSON file whose state-vector fields overwrite the measured ones each slot; the demo scenario hook, missing file means no injection",
		},
	},
	"CALM_PolicySelector": {
		Enums:        map[string][]string{"fallback_mode": calmModeNames},
		FilePath:     []string{"state_file"},
		OutputLabels: calmModeNames,
		Docs: map[string]string{
			"alpha":         "LinUCB exploration coefficient; takes effect only when this node creates the service's loop state, zero or less means unset",
			"state_file":    "optional JSON snapshot of the learner, written after each decision and restored once at startup; empty disables it",
			"fallback_mode": "mode emitted when no decision has ever been made and none can be made now",
		},
	},
	"CALM_PolicyMode": {
		Required: []string{"mode"},
		Enums: map[string][]string{
			"mode":              calmModeNames,
			"offloading_mode":   {calm.OffloadingLatencyPref, calm.OffloadingEnergyPref},
			"migration_allowed": {calm.MigrationTrue, calm.MigrationPenalized, calm.MigrationRestricted},
			"recovery_behavior": {calm.RecoveryBehaviorPersistentOnly, calm.RecoveryBehaviorNormal, calm.RecoveryBehaviorFallback},
			"node_selection":    {calm.NodeSelectionSpread, calm.NodeSelectionPack},
		},
		Docs: map[string]string{
			"mode":                     "which policy mode this instance represents; wire it to the selector output port of the same name",
			"scale_out_threshold":      "risk level above which the autoscaler adds replicas",
			"scale_in_threshold":       "load ratio below which the autoscaler may shed replicas",
			"scale_in_dmr_tolerance":   "fraction of the DMR SLO below which scale-in is still allowed",
			"redundancy_target":        "minimum replicas kept per service regardless of load",
			"redundancy_factor":        "workload-based floor multiplier; higher keeps more idle capacity",
			"offloading_mode":          "recorded in the plan only; offload execution is not implemented in v1",
			"offload_fraction":         "recorded in the plan only; offload execution is not implemented in v1",
			"migration_allowed":        "recorded in the plan only; migration execution is not implemented in v1",
			"migration_cost_weight":    "recorded in the plan only; migration execution is not implemented in v1",
			"recovery_behavior":        "which recovery action fires in SOFT and HARD states",
			"radio_compression_factor": "recorded in the plan only; the O3 radio lever is not implemented in v1",
			"node_selection":           "recorded in the plan only; node-level placement is not implemented in v1",
		},
	},
	"CALM_PlacementApply": {
		Docs: map[string]string{
			"dry_run":        "compute and report every change without issuing a PATCH; the safe demo rehearsal switch",
			"target_cluster": "manual override for the decision domain; normally the payload's target_cluster from the telemetry stage is used",
		},
	},
	"CALM_PostVerify": {
		Docs: map[string]string{
			"verify_window_seconds": "how far back the post-placement Jaeger re-read reaches; the evidence window for the SLO verdict and reward",
		},
	},
	"IEEE1935_Service": {
		Discriminator: "request",
		Enums:         map[string][]string{"request": {"List_Service", "Get_Service", "Instantiate_Service", "Service_Scaling", "Terminate_Service"}},
		ShowWhen: map[string][]string{
			"allocated_cpu":      {"Service_Scaling"},
			"allocated_memory":   {"Service_Scaling"},
			"allocated_replicas": {"Service_Scaling"},
			"app_name":           {"Get_Service", "Instantiate_Service", "Service_Scaling", "Terminate_Service"},
			"cluster_name":       {"List_Service", "Get_Service", "Instantiate_Service", "Service_Scaling", "Terminate_Service"},
			"selected_nodes":     {"Instantiate_Service"},
		},
	},
}

func init() {
	for _, r := range registrations {
		Schema[r.Type] = schema.Build("function", Description[r.Type], r, nodeMeta[r.Type])
	}
}
