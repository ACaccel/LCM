package lcmenabler

import (
	"errors"

	i "lcm/packages/interfaces"
	p "lcm/packages/policy"
)

type Service struct {
	SvcName string

	Config   i.ServiceConfig
	Policies map[string]*p.Policy
	Modules  map[string]i.ModuleConfig
}

func InitService(config i.ServiceConfig) *Service {
	var service Service = Service{
		SvcName:  config.SvcName,
		Config:   config,
		Policies: make(map[string]*p.Policy),
		Modules:  make(map[string]i.ModuleConfig),
	}

	for _, module := range config.Module {
		service.Modules[module.ModuleName] = module
	}

	return &service
}

func (s *Service) Configure(config i.ServiceConfig) error {
	s.Config = config

	s.Modules = make(map[string]i.ModuleConfig)
	for _, module := range config.Module {
		s.Modules[module.ModuleName] = module
	}

	return nil
}

func (s *Service) Terminate() error {
	if len(s.Policies) > 0 {
		for policyName, _ := range s.Policies {
			err := s.Undeploy(policyName)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) Deploy(policy string) error {
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
			StatusCh:  newPolicy.TriggerStatusCh[trigger.Description.NodeName],
		}
		go trigger.TriggerExecute(info)

		go newPolicy.Execute(trigger)
		newPolicy.StatusCh <- true
	}

	return nil
}

func (s *Service) Undeploy(policy string) error {
	p, ok := s.Policies[policy]
	if !ok {
		return errors.New("policy " + policy + " not found")
	}
	p.StatusCh <- false
	delete(s.Policies, policy)
	return nil
}

func (s *Service) SetDataPacket(policyName string, nodeName string, port int, transformat i.TransFormat) error {
	setup := false
	newConfig := s.Config
	for i, policy := range newConfig.Policy {
		if policy.PolicyName == policyName {
			for j, path := range newConfig.Policy[i].PolicyPath {
				if path.NodePair[0] == nodeName && 
					path.PortPair[0] == port {
					newConfig.Policy[i].PolicyPath[j].TransFormat = transformat
					setup = true
				}
			}
		}
	}
	
	// todo: modify module config

	if !setup {
		return errors.New("configure data packet failed")
	}
	
	s.Configure(newConfig)

	return nil
}