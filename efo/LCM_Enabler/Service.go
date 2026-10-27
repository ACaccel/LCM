package lcmenabler

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	lcmconfig "1935/efo/LCM_Enabler/config"
	i "1935/efo/LCM_Enabler/interfaces"
	u "1935/efo/LCM_Enabler/utils"
	rfi "1935/efo/Rule_Framework/interfaces"
)

type Service struct {
	SvcName string

	Config  i.ServiceConfig
	Modules map[string]i.ModuleConfig

	// rf is the runtime engine this service delegates policy execution to.
	rf rfi.IRule_framework
}

func InitService(config i.ServiceConfig, rf rfi.IRule_framework) *Service {
	// check empty policies and modules
	if config.Policy == nil {
		config.Policy = make([]i.PolicyConfig, 0)
	}
	if config.Module == nil {
		config.Module = make([]i.ModuleConfig, 0)
	}
	var service Service = Service{
		SvcName: config.SvcName,
		Config:  config,
		Modules: make(map[string]i.ModuleConfig),
		rf:      rf,
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
	return s.rf.TerminateService(s.SvcName)
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
	if s.rf.IsDeployed(s.SvcName, policy) {
		return errors.New("policy " + policy + " already deployed")
	}

	// check if the policy exists in the service config
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

	// delegate instantiation and execution to the runtime engine
	return s.rf.Deploy(s.SvcName, policyConfig, s.Modules)
}

func (s *Service) Undeploy(policy string) error {
	return s.rf.Undeploy(s.SvcName, policy)
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
	dir = filepath.Join(dir, lcmconfig.TemplatesDir())

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

/*
 * Modules
 *
 * A module is a service-level asset: it is defined once and instantiated in any
 * number of policies. Editing one therefore changes every policy that uses it,
 * which is why removing and shrinking are checked against the policies that
 * reference it rather than applied blindly.
 */

// ListModules names the service's modules.
func (s *Service) ListModules() []string {
	names := make([]string, 0, len(s.Config.Module))
	for _, module := range s.Config.Module {
		names = append(names, module.ModuleName)
	}
	return names
}

// ModuleInformation returns one module's stored config.
func (s *Service) ModuleInformation(moduleName string) (i.ModuleConfig, error) {
	for _, module := range s.Config.Module {
		if module.ModuleName == moduleName {
			return module, nil
		}
	}
	return i.ModuleConfig{}, errors.New("module " + moduleName + " not found")
}

// RegisterModule adds a module to the service.
func (s *Service) RegisterModule(module i.ModuleConfig) error {
	if module.ModuleName == "" {
		return errors.New("module name is empty")
	}
	if _, err := s.ModuleInformation(module.ModuleName); err == nil {
		return errors.New("module " + module.ModuleName + " already exists")
	}
	if err := s.validateModule(module); err != nil {
		return err
	}

	s.Config.Module = append(s.Config.Module, module)
	s.Modules[module.ModuleName] = module
	return nil
}

// UpdateModule replaces a module's definition.
//
// Renaming is not supported: the name is how policies refer to a module, so
// changing it here would leave every reference pointing at nothing. Delete and
// recreate instead, which forces the references to be dealt with.
func (s *Service) UpdateModule(moduleName string, module i.ModuleConfig) error {
	if module.ModuleName != moduleName {
		return errors.New("module name in the body is " + module.ModuleName +
			", which does not match " + moduleName + "; renaming is not supported")
	}
	if _, err := s.ModuleInformation(moduleName); err != nil {
		return err
	}
	if err := s.validateModule(module); err != nil {
		return err
	}
	if err := s.checkPortsStillReachable(module); err != nil {
		return err
	}

	for index, existing := range s.Config.Module {
		if existing.ModuleName == moduleName {
			s.Config.Module[index] = module
			break
		}
	}
	s.Modules[moduleName] = module
	return nil
}

// RemoveModule deletes a module that nothing refers to.
func (s *Service) RemoveModule(moduleName string) error {
	if _, err := s.ModuleInformation(moduleName); err != nil {
		return err
	}
	if users := s.policiesUsingModule(moduleName); len(users) != 0 {
		return errors.New("module " + moduleName + " is still used by " + strings.Join(users, ", "))
	}

	for index, existing := range s.Config.Module {
		if existing.ModuleName == moduleName {
			s.Config.Module = append(s.Config.Module[:index], s.Config.Module[index+1:]...)
			break
		}
	}
	delete(s.Modules, moduleName)
	return nil
}

// validateModule asks the engine whether the module could be deployed, with the
// candidate definition in place of whatever is stored.
func (s *Service) validateModule(module i.ModuleConfig) error {
	candidate := make(map[string]i.ModuleConfig, len(s.Modules)+1)
	for name, existing := range s.Modules {
		candidate[name] = existing
	}
	candidate[module.ModuleName] = module
	return s.rf.ValidateModule(module.ModuleName, candidate)
}

// checkPortsStillReachable refuses a boundary that drops a port a policy is
// still wired to.
//
// The designer recomputes a Module node's arity from the module, so a port that
// disappears takes its canvas handle with it, and the next save writes the
// policy back without that connection. Refusing here is what keeps a module edit
// from silently deleting an edge in a policy nobody had open.
func (s *Service) checkPortsStillReachable(module i.ModuleConfig) error {
	for _, policyConfig := range s.Config.Policy {
		for nodeName, referenced := range s.rf.ModuleReferences(policyConfig) {
			if referenced != module.ModuleName {
				continue
			}
			for _, path := range policyConfig.PolicyPath {
				if path.NodePair[0] == nodeName && path.PortPair[0] >= module.NumOutPort {
					return errors.New("policy " + policyConfig.PolicyName + " still connects " +
						nodeName + " output port " + strconv.Itoa(path.PortPair[0]) +
						", which this change removes")
				}
				if path.NodePair[1] == nodeName && path.PortPair[1] >= module.NumInPort {
					return errors.New("policy " + policyConfig.PolicyName + " still connects " +
						nodeName + " input port " + strconv.Itoa(path.PortPair[1]) +
						", which this change removes")
				}
			}
		}
	}
	return nil
}

// policiesUsingModule names the policies holding an instance of a module.
func (s *Service) policiesUsingModule(moduleName string) []string {
	users := []string{}
	for _, policyConfig := range s.Config.Policy {
		for _, referenced := range s.rf.ModuleReferences(policyConfig) {
			if referenced == moduleName {
				users = append(users, "policy "+policyConfig.PolicyName)
				break
			}
		}
	}
	for _, module := range s.Config.Module {
		if module.ModuleName == moduleName {
			continue
		}
		nested := s.rf.ModuleReferences(i.PolicyConfig{PolicyNode: module.ModuleNode})
		for _, referenced := range nested {
			if referenced == moduleName {
				users = append(users, "module "+module.ModuleName)
				break
			}
		}
	}
	return users
}
