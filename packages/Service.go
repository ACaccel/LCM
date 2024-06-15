package lcmenabler

import (
	"errors"

	i "lcm/packages/interfaces"
	p "lcm/packages/policy"
)

type Service struct {
	SvcName			string

	Config 			i.ServiceConfig
	Policies		map[string]p.Policy
	Modules			map[string]i.ModuleConfig
}

func InitService(config i.ServiceConfig) *Service {
	var service Service = Service{
		SvcName: config.SvcName,
		Config: config,
		Policies: make(map[string]p.Policy),
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
	s.Policies[policy] = newPolicy
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