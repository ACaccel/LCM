package lcmenabler

import (
	"errors"

	i "lcm/packages/interfaces"
	p "lcm/packages/policy"
)

type Service struct {
	SvcName			string

	Config 			i.ServiceConfig
	Policies		[]p.Policy
}

func InitService(config i.ServiceConfig) *Service {
	return &Service{
		SvcName: config.SvcName,
		Config: config,
	}
}

func (s *Service)Deploy(policy string) error {
	// check if policy already deployed
	for _, deployedPolicy := range s.Policies {
		if deployedPolicy.Description.PolicyName == policy {
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
	newPolicy.Init(policyConfig, s.Config.Module)
	s.Policies = append(s.Policies, newPolicy)
	return nil
}

func (s *Service)Undeploy(policy string) error {
	// check if the policy already deployed
	for index, deployedPolicy := range s.Policies {
		if deployedPolicy.Description.PolicyName == policy {
			s.Policies = append(s.Policies[:index], s.Policies[index+1:]...)
			return nil
		}
	}
	return errors.New("policy " + policy + " not found")
}