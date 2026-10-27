/*
Rule framework is responsible for dealing with all conditions, requirements,
constraints, attributes, and reaction policies that must be provided, maintained,
and/or enforced at runtime.

This is the runtime engine of the LCM feature. The LCM_Enabler (design/management)
holds the policy designs and delegates their deployment and execution here through
IRule_framework. The engine owns the registry of running policies and drives the
policy DAG (triggers + function nodes) defined in the policy/ and nodes/ packages.
*/
package ruleframework

import (
	"errors"

	lcmconfig "1935/efo/LCM_Enabler/config"
	lcm "1935/efo/LCM_Enabler/interfaces"
	rfconfig "1935/efo/Rule_Framework/config"
	abi "1935/efo/Rule_Framework/interfaces"
	"1935/efo/Rule_Framework/policy"
)

// Rule_Framework is the runtime engine. It holds the registry of deployed policies
// per service. Access mirrors the original single-map design (no added locking) to
// preserve runtime behavior.
type Rule_Framework struct {
	deployed map[string]map[string]*policy.Policy
}

var _ abi.IRule_framework = (*Rule_Framework)(nil)

// Build_Rule_Framework constructs an empty runtime engine.
func Build_Rule_Framework() *Rule_Framework {
	return &Rule_Framework{
		deployed: make(map[string]map[string]*policy.Policy),
	}
}

// Deploy instantiates a policy from its design config and starts its triggers and
// execution loop. The caller (LCM_Enabler) is responsible for the not-found check.
func (rf *Rule_Framework) Deploy(svcName string, cfg lcm.PolicyConfig, modules map[string]lcm.ModuleConfig) error {
	if rf.deployed[svcName] == nil {
		rf.deployed[svcName] = make(map[string]*policy.Policy)
	}
	if _, ok := rf.deployed[svcName][cfg.PolicyName]; ok {
		return errors.New("policy " + cfg.PolicyName + " already deployed")
	}

	var newPolicy policy.Policy
	if err := newPolicy.Init(cfg, modules); err != nil {
		return err
	}
	rf.deployed[svcName][cfg.PolicyName] = &newPolicy

	// Attach the execution recorder before any walker goroutine starts, so the
	// per-node recorder fields are immutable for the policy's whole lifetime.
	newPolicy.AttachRecorder(policy.NewRecorder(cfg.PolicyName))

	deployInfo := abi.ExecInfo{
		Service_Name:        svcName,
		File_Path:           lcmconfig.UploadDir() + "/" + svcName,
		ControlPlaneBaseURL: rfconfig.ControlPlaneBaseURL(),
	}

	// One loop for the policy, not one per trigger: the loop owns the policy's
	// lifecycle and its single set of data packets, both of which are singular.
	go newPolicy.Execute(deployInfo)

	for _, trigger := range newPolicy.Triggers {
		triggerInfo := deployInfo
		triggerInfo.WebhookCh = newPolicy.WebhookChMap[trigger.Description.NodeName]
		triggerInfo.TriggerCh = newPolicy.TriggerCh
		triggerInfo.StopCh = newPolicy.StopCh()
		newPolicy.StartTrigger(trigger, triggerInfo)
	}

	// Blocks until Execute is listening, so Deploy does not report a policy
	// running before it can receive anything.
	newPolicy.StartCh <- true

	return nil
}

// Undeploy stops and removes a deployed policy.
func (rf *Rule_Framework) Undeploy(svcName, policyName string) error {
	policies, ok := rf.deployed[svcName]
	if !ok {
		return errors.New("policy " + policyName + " not found")
	}
	p, ok := policies[policyName]
	if !ok {
		return errors.New("policy " + policyName + " not found")
	}
	p.Stop()
	delete(policies, policyName)
	return nil
}

// IsDeployed reports whether a policy is currently running.
func (rf *Rule_Framework) IsDeployed(svcName, policyName string) bool {
	policies, ok := rf.deployed[svcName]
	if !ok {
		return false
	}
	_, ok = policies[policyName]
	return ok
}

// DeployedPolicies lists the names of a service's running policies.
func (rf *Rule_Framework) DeployedPolicies(svcName string) []string {
	names := make([]string, 0, len(rf.deployed[svcName]))
	for name := range rf.deployed[svcName] {
		names = append(names, name)
	}
	return names
}

// ExecuteWebhook dispatches content to every running webhook trigger subscribed to
// eventType in the service, returning the number dispatched. Subscriptions are keyed
// by each webhook node's event_type property (see policy.Policy.WebhookSubs).
func (rf *Rule_Framework) ExecuteWebhook(svcName, eventType string, content any) (int, error) {
	dispatched := 0
	for _, p := range rf.deployed[svcName] {
		for _, ch := range p.WebhookSubs[eventType] {
			ch <- content
			dispatched++
		}
	}
	return dispatched, nil
}

// TerminateService undeploys all running policies of a service.
// LatestRun returns the latest execution trace of a deployed policy. Events are
// filtered to those newer than since so a GUI can poll incrementally.
func (rf *Rule_Framework) LatestRun(svcName, policyName string, since int) (lcm.RunTrace, error) {
	policies, ok := rf.deployed[svcName]
	if !ok {
		return lcm.RunTrace{}, errors.New("policy " + policyName + " not found")
	}
	p, ok := policies[policyName]
	if !ok {
		return lcm.RunTrace{}, errors.New("policy " + policyName + " not found")
	}
	return p.LatestRun(since), nil
}

func (rf *Rule_Framework) TerminateService(svcName string) error {
	for policyName := range rf.deployed[svcName] {
		if err := rf.Undeploy(svcName, policyName); err != nil {
			return err
		}
	}
	return nil
}

// ValidateModule builds the module and discards it; see policy.ValidateModule.
func (rf *Rule_Framework) ValidateModule(moduleName string, modules map[string]lcm.ModuleConfig) error {
	return policy.ValidateModule(moduleName, modules)
}

// ModuleReferences maps each Module node in a policy to the module it names.
func (rf *Rule_Framework) ModuleReferences(cfg lcm.PolicyConfig) map[string]string {
	references := make(map[string]string)
	for _, node := range cfg.PolicyNode {
		if node.NodeType == "Module" {
			references[node.NodeName] = policy.ModuleNameOf(node)
		}
	}
	return references
}
