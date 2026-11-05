package resource

import "github.com/ACaccel/lcm/internal/message"

// Reasons why an action, capability or operation is unavailable or in its
// current state. Components store and return the text; ReasonMessage gives
// each a code so that clients can translate it.
const (
	MachineNotManaged         = "machine is not managed"
	RouterNotManaged          = "router is not managed"
	MachineNotRetired         = "machine is not retired"
	RouterNotRetired          = "router is not retired"
	RetireMachineFirst        = "retire the machine first"
	RetireRouterFirst         = "retire the router first"
	MachineUnreadable         = "machine state could not be read"
	RouterUnreadable          = "router state could not be read"
	MachineUnavailable        = "machine is not available"
	RouterUnavailable         = "router is not available"
	ResolveOperationFirst     = "resolve the outstanding operation first"
	ExitMaintenanceFirst      = "exit maintenance first"
	ReleaseOwnershipFirst     = "release Git ownership first"
	OperationUnresolved       = "an unresolved operation already exists"
	NoObserver                = "no observer is configured"
	MaintenanceActive         = "maintenance is already active"
	MaintenanceInactive       = "maintenance is not active"
	NoPowerHelper             = "restricted helper is not configured"
	MachineRetired            = "machine is retired"
	PowerNeedsSystemd         = "power control requires a verified Linux/systemd host"
	PowerNeedsFresh           = "fresh verified observations are required"
	PowerNeedsHealthy         = "required health checks must be healthy"
	PowerNeedsEvidence        = "required OS evidence sources are unavailable"
	PowerPreflightFailed      = "power control is disabled or its helper/autostart preflight failed"
	NoPowerOn                 = "no independent power-on channel; manual power-on is required"
	ConnectionNoReboot        = "connection does not allow reboot"
	NeedsObservation          = "a fresh successful observation is required"
	NoUptime                  = "the router does not report its uptime"
	LocalMaintenance          = "local machine maintenance is active"
	PasswordSecretUnavailable = "password secret is unavailable"
	ConnectionSettingsInvalid = "connection settings are invalid"
	HelperPrepareFailed       = "helper preparation failed"
	HelperNoPrepared          = "helper preparation did not establish a new prepared receipt"
	PreconditionsChanged      = "required preconditions changed before handoff"
	HelperUnverified          = "helper submission could not be verified"
	HelperNoAcceptance        = "helper did not verify OS request acceptance"
	NoPowerOffEvidence        = "accepted request has no independent power-off evidence"
	HelperDownOnRecovery      = "helper unavailable during recovery"
	HelperNoReceipt           = "helper receipt unavailable"
	BootVerified              = "new boot and fresh required health verified"
	NoCompletionEvidence      = "operation lacks verified completion evidence"
	NotSentAudit              = "request was not sent: its audit entry could not be stored"
	NotSentState              = "request was not sent: its state could not be stored"
	RouterAccepted            = "router accepted the request; waiting for reboot evidence"
	OutcomeUnknown            = "submission outcome unknown; waiting for reboot evidence"
	RouterRejected            = "router rejected the request"
	NotDelivered              = "request was not delivered"
	NotDeliveredAuth          = "request was not delivered: authentication rejected"
	NeverSent                 = "request was never sent: submission stopped before sending"
	InterruptedBeforeOutcome  = "interrupted before the submission outcome was recorded; never resubmitted"
	RouterBooted              = "router booted after the request and answers again"
	NoRebootEvidence          = "no reboot evidence before the deadline"
)

// unavailableReasons maps the reasons above to their messages.
var unavailableReasons = map[string]message.Message{
	MachineNotManaged:         message.New("action.notManaged", MachineNotManaged, "type", "machine"),
	RouterNotManaged:          message.New("action.notManaged", RouterNotManaged, "type", "router"),
	MachineNotRetired:         message.New("action.notRetired", MachineNotRetired, "type", "machine"),
	RouterNotRetired:          message.New("action.notRetired", RouterNotRetired, "type", "router"),
	RetireMachineFirst:        message.New("action.retireFirst", RetireMachineFirst, "type", "machine"),
	RetireRouterFirst:         message.New("action.retireFirst", RetireRouterFirst, "type", "router"),
	MachineUnreadable:         message.New("action.stateUnreadable", MachineUnreadable, "type", "machine"),
	RouterUnreadable:          message.New("action.stateUnreadable", RouterUnreadable, "type", "router"),
	MachineUnavailable:        message.New("action.unavailable", MachineUnavailable, "type", "machine"),
	RouterUnavailable:         message.New("action.unavailable", RouterUnavailable, "type", "router"),
	ResolveOperationFirst:     message.New("action.resolveFirst", ResolveOperationFirst),
	ExitMaintenanceFirst:      message.New("action.exitMaintenanceFirst", ExitMaintenanceFirst),
	ReleaseOwnershipFirst:     message.New("action.releaseOwnershipFirst", ReleaseOwnershipFirst),
	OperationUnresolved:       message.New("action.operationUnresolved", OperationUnresolved),
	NoObserver:                message.New("action.noObserver", NoObserver),
	MaintenanceActive:         message.New("action.maintenanceActive", MaintenanceActive),
	MaintenanceInactive:       message.New("action.maintenanceInactive", MaintenanceInactive),
	NoPowerHelper:             message.New("capability.noHelper", NoPowerHelper),
	MachineRetired:            message.New("capability.machineRetired", MachineRetired),
	PowerNeedsSystemd:         message.New("capability.needsSystemd", PowerNeedsSystemd),
	PowerNeedsFresh:           message.New("capability.needsFresh", PowerNeedsFresh),
	PowerNeedsHealthy:         message.New("capability.needsHealthy", PowerNeedsHealthy),
	PowerNeedsEvidence:        message.New("capability.needsEvidence", PowerNeedsEvidence),
	PowerPreflightFailed:      message.New("capability.preflightFailed", PowerPreflightFailed),
	NoPowerOn:                 message.New("capability.noPowerOn", NoPowerOn),
	ConnectionNoReboot:        message.New("capability.connectionNoReboot", ConnectionNoReboot),
	NeedsObservation:          message.New("capability.needsObservation", NeedsObservation),
	NoUptime:                  message.New("capability.noUptime", NoUptime),
	LocalMaintenance:          message.New("capability.localMaintenance", LocalMaintenance),
	PasswordSecretUnavailable: message.New("snapshot.noPassword", PasswordSecretUnavailable),
	ConnectionSettingsInvalid: message.New("snapshot.badConnection", ConnectionSettingsInvalid),
	HelperPrepareFailed:       message.New("op.helperPrepareFailed", HelperPrepareFailed),
	HelperNoPrepared:          message.New("op.helperNoPrepared", HelperNoPrepared),
	PreconditionsChanged:      message.New("op.preconditionsChanged", PreconditionsChanged),
	HelperUnverified:          message.New("op.helperUnverified", HelperUnverified),
	HelperNoAcceptance:        message.New("op.helperNoAcceptance", HelperNoAcceptance),
	NoPowerOffEvidence:        message.New("op.noPowerOffEvidence", NoPowerOffEvidence),
	HelperDownOnRecovery:      message.New("op.helperDownOnRecovery", HelperDownOnRecovery),
	HelperNoReceipt:           message.New("op.helperNoReceipt", HelperNoReceipt),
	BootVerified:              message.New("op.bootVerified", BootVerified),
	NoCompletionEvidence:      message.New("op.noCompletionEvidence", NoCompletionEvidence),
	NotSentAudit:              message.New("op.notSentAudit", NotSentAudit),
	NotSentState:              message.New("op.notSentState", NotSentState),
	RouterAccepted:            message.New("op.routerAccepted", RouterAccepted),
	OutcomeUnknown:            message.New("op.outcomeUnknown", OutcomeUnknown),
	RouterRejected:            message.New("op.routerRejected", RouterRejected),
	NotDelivered:              message.New("op.notDelivered", NotDelivered),
	NotDeliveredAuth:          message.New("op.notDeliveredAuth", NotDeliveredAuth),
	NeverSent:                 message.New("op.neverSent", NeverSent),
	InterruptedBeforeOutcome:  message.New("op.interruptedBeforeOutcome", InterruptedBeforeOutcome),
	RouterBooted:              message.New("op.routerBooted", RouterBooted),
	NoRebootEvidence:          message.New("op.noRebootEvidence", NoRebootEvidence),
}
