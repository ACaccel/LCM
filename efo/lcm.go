package efo

import (
	"os"

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

func (E *EFO) Execute_Webhook(service_name string, event_type string, content any) error {
	err := E.LCM_Enabler.Execute_Webhook(service_name, event_type, content)
	if err != nil {
		return err
	}

	return nil
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

func (E *EFO) Register_Service_Config(dst string) error {
	var config i.ServiceConfig
	if err := utils.ReadYaml(dst, &config); err != nil {
		return err
	}
	rename_dst := "./efo/LCM_Enabler/upload/" + config.SvcName + ".yaml"
	if err := os.Rename(dst, rename_dst); err != nil {
		return err
	}

	if err := E.LCM_Enabler.Register_Service(config); err != nil {
		return err
	}

	return nil
}

func (E *EFO) Configure_Service_Config(dst string) error {
	var config i.ServiceConfig
	if err := utils.ReadYaml(dst, &config); err != nil {
		return err
	}
	rename_dst := "./efo/LCM_Enabler/upload/" + config.SvcName + ".yaml"
	if err := os.Rename(dst, rename_dst); err != nil {
		return err
	}

	if err := E.LCM_Enabler.Configure_Service(config.SvcName, config); err != nil {
		return err
	}

	return nil
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

func (E *EFO) DataPacket_Information(service string, policy string, node string, port int) (any, error) {
	info, err := E.LCM_Enabler.DataPacket_Information(service, policy, node, port)
	if err != nil {
		return nil, err
	}

	return info, nil
}

func (E *EFO) Configure_DataPacket(service string, policy string, datapacket i.ConfigureDataPacket) error {
	err := E.LCM_Enabler.Configure_DataPacket(service, policy, datapacket)
	if err != nil {
		return err
	}

	return nil
}

func (E *EFO) Configure_Template(service string, template string) error {
	err := E.LCM_Enabler.Configure_Template(service, template)
	if err != nil {
		return err
	}

	return nil
}
