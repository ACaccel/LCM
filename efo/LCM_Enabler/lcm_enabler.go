package lcmenabler

import (
	"errors"
	"sort"

	i "1935/efo/LCM_Enabler/interfaces"
	u "1935/efo/LCM_Enabler/utils"
	functions "1935/efo/Rule_Framework/nodes/Functions"
	triggers "1935/efo/Rule_Framework/nodes/Triggers"
	rfi "1935/efo/Rule_Framework/interfaces"
)

type ILCM_Enabler interface {
	Execute_Webhook()
	Register_Service()
	Deploy_Policy()
	Undeploy_Policy()
	Configure_Service()
}

type LCM_Enabler struct {
	Services map[string]*Service

	// rf is the runtime engine that LCM_Enabler delegates policy execution to.
	rf rfi.IRule_framework
}

// Build_LCM constructs the LCM_Enabler with an injected runtime engine.
func Build_LCM(rf rfi.IRule_framework) *LCM_Enabler {
	return &LCM_Enabler{
		Services: make(map[string]*Service),
		rf:       rf,
	}
}

func (l *LCM_Enabler) Service_Information(svcName string) (any, error) {
	if _, ok := l.Services[svcName]; !ok {
		return nil, errors.New("service " + svcName + " not found")
	}

	svc_config := l.Services[svcName].Config
	config, err := u.SendMapData(svc_config)
	if err != nil {
		return nil, err
	}

	return config, nil
}

func (l *LCM_Enabler) Register_Service(config i.ServiceConfig) error {
	if _, ok := l.Services[config.SvcName]; ok {
		return errors.New("service " + config.SvcName + " already exists")
	}
	l.Services[config.SvcName] = InitService(config, l.rf)

	return nil
}

func (l *LCM_Enabler) Configure_Service(svcName string, config i.ServiceConfig) error {
	if _, ok := l.Services[svcName]; !ok {
		return errors.New("service " + svcName + " not found")
	}

	if len(l.rf.DeployedPolicies(svcName)) != 0 {
		return errors.New("service " + svcName + " has deployed policies")
	}

	err := l.Services[svcName].Configure(config)
	if err != nil {
		return err
	}

	return nil
}

func (l *LCM_Enabler) Terminate_Service(svcName string) error {
	if _, ok := l.Services[svcName]; !ok {
		return errors.New("service " + svcName + " not found")
	}

	err := l.Services[svcName].Terminate()
	if err != nil {
		return err
	}
	delete(l.Services, svcName)

	return nil
}

// information of deployed policies
func (l *LCM_Enabler) Policy_Information(svcName string, policyName string) (i.PolicyInfo, error) {
	if _, ok := l.Services[svcName]; !ok {
		return i.PolicyInfo{}, errors.New("service " + svcName + " not found")
	}

	var pConfig i.PolicyConfig = i.PolicyConfig{}
	for _, config := range l.Services[svcName].Config.Policy {
		if config.PolicyName == policyName {
			pConfig = config
			break
		}
	}
	if pConfig.PolicyName == "" {
		return i.PolicyInfo{}, errors.New("policy " + policyName + " not found")
	}

	config, err := u.SendMapData(pConfig)
	if err != nil {
		return i.PolicyInfo{}, err
	}

	var status string = "UNDEPLOYED"
	if l.rf.IsDeployed(svcName, policyName) {
		status = "DEPLOYED"
	}

	return i.PolicyInfo{
		Name:   policyName,
		Status: status,
		Config: config,
	}, nil
}

func (l *LCM_Enabler) Register_Policy(svcName string, policy i.PolicyConfig) error {
	if _, ok := l.Services[svcName]; !ok {
		return errors.New("service " + svcName + " not found")
	}

	err := l.Services[svcName].RegisterPolicy(policy)
	if err != nil {
		return err
	}

	return nil
}

func (l *LCM_Enabler) Update_Policy(svcName string, policyName string, policy i.PolicyConfig) error {
	if _, ok := l.Services[svcName]; !ok {
		return errors.New("service " + svcName + " not found")
	}

	err := l.Services[svcName].UpdatePolicy(policyName, policy)
	if err != nil {
		return err
	}

	return nil
}

func (l *LCM_Enabler) Remove_Policy(svcName string, policyName string) error {
	if _, ok := l.Services[svcName]; !ok {
		return errors.New("service " + svcName + " not found")
	}

	err := l.Services[svcName].RemovePolicy(policyName)
	if err != nil {
		return err
	}

	return nil
}

func (l *LCM_Enabler) Deploy_Policy(svcName string, policies []string) error {
	for _, policyName := range policies {
		err := l.Services[svcName].Deploy(policyName)
		if err != nil {
			return err
		}
	}
	return nil
}

func (l *LCM_Enabler) Undeploy_Policy(svcName string, policies []string) error {
	for _, policyName := range policies {
		err := l.Services[svcName].Undeploy(policyName)
		if err != nil {
			return err
		}
	}
	return nil
}

// Run_Information returns the latest execution trace of a deployed policy so a
// design GUI can show which nodes ran and what each of them produced. Events are
// filtered to those newer than since, letting a client poll incrementally.
func (l *LCM_Enabler) Run_Information(svcName string, policyName string, since int) (i.RunTrace, error) {
	if _, ok := l.Services[svcName]; !ok {
		return i.RunTrace{}, errors.New("service " + svcName + " not found")
	}

	if !l.rf.IsDeployed(svcName, policyName) {
		return i.RunTrace{}, errors.New("policy " + policyName + " not found")
	}

	return l.rf.LatestRun(svcName, policyName, since)
}

func (l *LCM_Enabler) Configure_Template(svcName string, templateName string) error {
	if _, ok := l.Services[svcName]; !ok {
		return errors.New("service " + svcName + " not found")
	}

	err := l.Services[svcName].UseTemplate(templateName)
	if err != nil {
		return err
	}

	return nil
}

func (l *LCM_Enabler) Execute_Webhook(svcName string, event_type string, content any) (int, error) {
	if _, ok := l.Services[svcName]; !ok {
		return 0, errors.New("service " + svcName + " not found")
	}
	return l.rf.ExecuteWebhook(svcName, event_type, content)
}

// List_Services returns the names of all registered services.
func (l *LCM_Enabler) List_Services() []string {
	names := make([]string, 0, len(l.Services))
	for name := range l.Services {
		names = append(names, name)
	}
	return names
}

// Node_Types returns the available trigger and function node types so a design
// GUI can populate its node palette and render each node without hard-coding
// the catalog, the field names or the legal property values.
//
// The schemas list carries the field-level detail; triggers and functions keep
// their original shape so existing clients are unaffected. Everything is sorted
// by type: Go map iteration is random, and an unordered response would reshuffle
// the palette on every request and defeat any byte-level comparison.
func (l *LCM_Enabler) Node_Types() map[string]any {
	trig := make([]map[string]any, 0, len(triggers.Description))
	for name, d := range triggers.Description {
		trig = append(trig, map[string]any{
			"type":        name,
			"triggerType": d.TriggerType,
		})
	}
	fn := make([]map[string]any, 0, len(functions.Description))
	for name, d := range functions.Description {
		fn = append(fn, map[string]any{
			"type":          name,
			"numInputPort":  d.NumInputPort,
			"numOutputPort": d.NumOutputPort,
		})
	}
	schemas := make([]i.NodeSchema, 0, len(functions.Schema)+len(triggers.Schema))
	for _, s := range functions.Schema {
		schemas = append(schemas, s)
	}
	for _, s := range triggers.Schema {
		schemas = append(schemas, s)
	}

	sortByType(trig)
	sortByType(fn)
	sort.Slice(schemas, func(a, b int) bool { return schemas[a].Type < schemas[b].Type })

	return map[string]any{"triggers": trig, "functions": fn, "schemas": schemas}
}

func sortByType(entries []map[string]any) {
	sort.Slice(entries, func(a, b int) bool {
		x, _ := entries[a]["type"].(string)
		y, _ := entries[b]["type"].(string)
		return x < y
	})
}

/*
 * Modules
 */

func (l *LCM_Enabler) List_Modules(svcName string) ([]string, error) {
	if _, ok := l.Services[svcName]; !ok {
		return nil, errors.New("service " + svcName + " not found")
	}
	return l.Services[svcName].ListModules(), nil
}

func (l *LCM_Enabler) Module_Information(svcName string, moduleName string) (any, error) {
	if _, ok := l.Services[svcName]; !ok {
		return nil, errors.New("service " + svcName + " not found")
	}

	module, err := l.Services[svcName].ModuleInformation(moduleName)
	if err != nil {
		return nil, err
	}

	return u.SendMapData(module)
}

func (l *LCM_Enabler) Register_Module(svcName string, module i.ModuleConfig) error {
	if _, ok := l.Services[svcName]; !ok {
		return errors.New("service " + svcName + " not found")
	}
	return l.Services[svcName].RegisterModule(module)
}

func (l *LCM_Enabler) Update_Module(svcName string, moduleName string, module i.ModuleConfig) error {
	if _, ok := l.Services[svcName]; !ok {
		return errors.New("service " + svcName + " not found")
	}
	return l.Services[svcName].UpdateModule(moduleName, module)
}

func (l *LCM_Enabler) Remove_Module(svcName string, moduleName string) error {
	if _, ok := l.Services[svcName]; !ok {
		return errors.New("service " + svcName + " not found")
	}
	return l.Services[svcName].RemoveModule(moduleName)
}
