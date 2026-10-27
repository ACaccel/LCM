package efo

import (
	"errors"
	"os"

	lcmconfig "1935/efo/LCM_Enabler/config"
	i "1935/efo/LCM_Enabler/interfaces"
	utils "1935/efo/LCM_Enabler/utils"
)

func (E *EFO) Service_Information(svcName string) (any, error) {
	info, err := E.LCM_Enabler.Service_Information(svcName)
	if err != nil {
		return nil, err
	}

	return info, nil
}

func (E *EFO) Execute_Webhook(service_name string, event_type string, content any) (int, error) {
	return E.LCM_Enabler.Execute_Webhook(service_name, event_type, content)
}

func (E *EFO) Register_Policy(service_name string, policy i.PolicyConfig) error {
	err := E.LCM_Enabler.Register_Policy(service_name, policy)
	if err != nil {
		return err
	}

	return nil
}

func (E *EFO) Update_Policy(service_name string, policy_name string, policy i.PolicyConfig) error {
	err := E.LCM_Enabler.Update_Policy(service_name, policy_name, policy)
	if err != nil {
		return err
	}

	return nil
}

func (E *EFO) Remove_Policy(service_name string, policy_name string) error {
	err := E.LCM_Enabler.Remove_Policy(service_name, policy_name)
	if err != nil {
		return err
	}

	return nil
}

func (E *EFO) Policy_Deployment(deploy_data i.DeployPolicy) error {
	if err := E.LCM_Enabler.Deploy_Policy(deploy_data.SvcName, deploy_data.Policies); err != nil {
		return err
	}

	return nil
}

func (E *EFO) Policy_Undeployment(deploy_data i.DeployPolicy) error {
	if err := E.LCM_Enabler.Undeploy_Policy(deploy_data.SvcName, deploy_data.Policies); err != nil {
		return err
	}

	return nil
}

func (E *EFO) Register_Service(config i.ServiceConfig) error {
	err := E.LCM_Enabler.Register_Service(config)
	if err != nil {
		return err
	}

	return nil
}

func (E *EFO) Configure_Service(svcName string, config i.ServiceConfig) error {
	err := E.LCM_Enabler.Configure_Service(svcName, config)
	if err != nil {
		return err
	}

	return nil
}

// Register_Service_Config parses an uploaded YAML config file and registers it
// as a new service. The parsed config is returned so the API layer can tell the
// caller what was created (service name, policy names) without re-reading the
// file.
func (E *EFO) Register_Service_Config(dst string) (i.ServiceConfig, error) {
	config, err := readServiceConfig(dst)
	if err != nil {
		return i.ServiceConfig{}, err
	}

	if err := E.LCM_Enabler.Register_Service(config); err != nil {
		return i.ServiceConfig{}, err
	}

	return config, nil
}

// Configure_Service_Config is the reconfigure counterpart of
// Register_Service_Config: the uploaded document replaces the service's whole
// config.
func (E *EFO) Configure_Service_Config(dst string) (i.ServiceConfig, error) {
	config, err := readServiceConfig(dst)
	if err != nil {
		return i.ServiceConfig{}, err
	}

	if err := E.LCM_Enabler.Configure_Service(config.SvcName, config); err != nil {
		return i.ServiceConfig{}, err
	}

	return config, nil
}

// readServiceConfig parses an uploaded service config and archives the file as
// <upload dir>/<service_name>.yaml, the same side effect both upload endpoints
// have always had.
func readServiceConfig(dst string) (i.ServiceConfig, error) {
	var config i.ServiceConfig
	if err := utils.ReadYaml(dst, &config); err != nil {
		return i.ServiceConfig{}, err
	}
	// An empty name would archive to "/.yaml" and register an unaddressable
	// service; reject it here where the message can point at the document.
	if config.SvcName == "" {
		return i.ServiceConfig{}, errors.New("config is missing service_name")
	}
	rename_dst := "./" + lcmconfig.UploadDir() + "/" + config.SvcName + ".yaml"
	if err := os.Rename(dst, rename_dst); err != nil {
		return i.ServiceConfig{}, err
	}
	return config, nil
}

func (E *EFO) Remove_Service(svcName string) error {
	err := E.LCM_Enabler.Terminate_Service(svcName)
	if err != nil {
		return err
	}

	return nil
}

func (E *EFO) Policy_Information(svcName string, policyName string) (any, error) {
	info, err := E.LCM_Enabler.Policy_Information(svcName, policyName)
	if err != nil {
		return nil, err
	}

	return info, nil
}

func (E *EFO) Configure_Template(service string, template string) error {
	err := E.LCM_Enabler.Configure_Template(service, template)
	if err != nil {
		return err
	}

	return nil
}

func (E *EFO) Policy_Run(service string, policy string, since int) (i.RunTrace, error) {
	return E.LCM_Enabler.Run_Information(service, policy, since)
}

func (E *EFO) List_Services() []string {
	return E.LCM_Enabler.List_Services()
}

func (E *EFO) Node_Types() map[string]any {
	return E.LCM_Enabler.Node_Types()
}

func (E *EFO) List_Modules(service_name string) ([]string, error) {
	return E.LCM_Enabler.List_Modules(service_name)
}

func (E *EFO) Module_Information(service_name string, module_name string) (any, error) {
	return E.LCM_Enabler.Module_Information(service_name, module_name)
}

func (E *EFO) Register_Module(service_name string, module i.ModuleConfig) error {
	return E.LCM_Enabler.Register_Module(service_name, module)
}

func (E *EFO) Update_Module(service_name string, module_name string, module i.ModuleConfig) error {
	return E.LCM_Enabler.Update_Module(service_name, module_name, module)
}

func (E *EFO) Remove_Module(service_name string, module_name string) error {
	return E.LCM_Enabler.Remove_Module(service_name, module_name)
}
