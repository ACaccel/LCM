package lcmenabler

import (
	"errors"

	i "lcm/packages/interfaces"
	p "lcm/packages/policy"
)

type Service struct {
	SvcName			string

	Config 			i.ServiceConfig
	Policies		map[string]*p.Policy
	Modules			map[string]i.ModuleConfig
}

func InitService(config i.ServiceConfig) *Service {
	var service Service = Service{
		SvcName: config.SvcName,
		Config: config,
		Policies: make(map[string]*p.Policy),
		Modules: make(map[string]i.ModuleConfig),
	}

	for _, module := range config.Module {
		service.Modules[module.ModuleName] = module
	}

	return &service
}

func (s *Service)Deploy(policy string) error {
	// check if policy already deployed
	for policyName, _ := range s.Policies {
		if policyName == policy {
			return errors.New("policy " + policy + " already deployed")
		}
	}

	// check if the policy exists in database
	var policyConfig i.PolicyConfig = i.PolicyConfig{}
	for _, config := range s.Config.Policy {
		if config.PolicyName == policy {
			policyConfig = config
			break
		}
	}
	if policyConfig.PolicyName == "" {
		return errors.New("policy " + policy + " not found")
	}

	var newPolicy p.Policy = p.Policy{}
	newPolicy.Init(policyConfig, s.Modules)
	s.Policies[policy] = &newPolicy

	// execute the trigger node and the policy
	for _, trigger := range newPolicy.Triggers {
		info := i.ExecInfo{
			WebhookCh: newPolicy.WebhookChMap[trigger.Description.NodeName],
			TriggerCh: newPolicy.TriggerCh,
			StatusCh: newPolicy.StatusCh,
		}
		go trigger.TriggerExecute(info)

		newPolicy.Status = true
		go newPolicy.Execute(trigger)
	}

	return nil
}

func (s *Service)Undeploy(policy string) error {
	// check if the policy already deployed
	for policyName, _ := range s.Policies {
		if policyName == policy {
			delete(s.Policies, policy)
			return nil
		}
	}
	return errors.New("policy " + policy + " not found")
}

func (s *Service)Configure(config i.ServiceConfig) error {
	// check if any policy is deployed
	if len(s.Policies) > 0 {
		return errors.New("service has deployed policies")
	}
	
	// configuration
	s.Config = config
	
	s.Modules = make(map[string]i.ModuleConfig)
	for _, module := range config.Module {
		s.Modules[module.ModuleName] = module
	}

	return nil
}