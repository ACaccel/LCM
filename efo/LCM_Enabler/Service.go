package lcmenabler

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	i "1935/efo/LCM_Enabler/interfaces"
	p "1935/efo/LCM_Enabler/policy"
	u "1935/efo/LCM_Enabler/utils"
)

type Service struct {
	SvcName string

	Config   i.ServiceConfig
	Policies map[string]*p.Policy
	Modules  map[string]i.ModuleConfig
}

func InitService(config i.ServiceConfig) *Service {
	// check empty policies and modules
	if config.Policy == nil {
		config.Policy = make([]i.PolicyConfig, 0)
	}
	if config.Module == nil {
		config.Module = make([]i.ModuleConfig, 0)
	}
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
		for policyName := range s.Policies {
			err := s.Undeploy(policyName)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) RegisterPolicy(policy i.PolicyConfig) error {
	// check if policy already exists
	for _, config := range s.Config.Policy {
		if config.PolicyName == policy.PolicyName {
			return errors.New("policy " + policy.PolicyName + " already exists")
		}
	}

	s.Config.Policy = append(s.Config.Policy, policy)
	return nil
}

func (s *Service) UpdatePolicy(policyName string, newPolicy i.PolicyConfig) error {
	// check if policy already exists in service config
	var policyExists bool = false
	for i, config := range s.Config.Policy {
		if config.PolicyName == policyName {
			policyExists = true
			s.Config.Policy[i] = newPolicy
			break
		}
	}
	if !policyExists {
		return errors.New("policy " + policyName + " not found")
	}

	return nil
}

func (s *Service) RemovePolicy(policyName string) error {
	// check if policy already exists in service config
	var policyExists bool = false
	for i, config := range s.Config.Policy {
		if config.PolicyName == policyName {
			policyExists = true
			s.Config.Policy = append(s.Config.Policy[:i], s.Config.Policy[i+1:]...)
			break
		}
	}
	if !policyExists {
		return errors.New("policy " + policyName + " not found")
	}

	return nil
}

func (s *Service) Deploy(policy string) error {
	// check if policy already deployed
	for policyName := range s.Policies {
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
	err := newPolicy.Init(policyConfig, s.Modules)
	if err != nil {
		return err
	}
	s.Policies[policy] = &newPolicy

	// execute the trigger node and the policy
	for _, trigger := range newPolicy.Triggers {
		info := i.ExecInfo{
			Service_Name: s.SvcName,
			File_Path:	"efo/LCM_Enabler/upload/" + s.SvcName,
			WebhookCh: newPolicy.WebhookChMap[trigger.Description.NodeName],
			TriggerCh: newPolicy.TriggerCh,
			StatusCh:  newPolicy.TriggerStatusCh[trigger.Description.NodeName],
		}
		go trigger.TriggerExecute(info)

		go newPolicy.Execute(trigger, info)
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

func (s *Service) UseTemplate(template_name string) error {
	var template i.Template

	if template_name == "" {
		return errors.New("template name is empty")
	}

	// search existing templates
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	dir = filepath.Join(dir, "efo", "LCM_Enabler", "templates")

	files, err := os.ReadDir(dir)
	if err != nil {
		return err
	}

	for _, f := range files {
		if f.IsDir() {
			continue
		}

		filePath := filepath.Join(dir, f.Name())
		if strings.Split(f.Name(), ".")[0] == template_name {
			err := u.ReadYaml(filePath, &template)
			if err != nil {
				return err
			}

			// check template
			if !(template.SvcName == s.SvcName || template.SvcName == "default") {
				return errors.New("no permission to use this template")
			}

			break
		}
	}
	newConfig := s.Config
	newConfig.Policy = append(newConfig.Policy, template.Policy...)
	s.Configure(newConfig)

	return nil
}