package lcmenabler

import (
	"errors"

	i "lcm/packages/interfaces"
	u "lcm/packages/utils"
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
}

func Build_LCM() *LCM_Enabler {
	return &LCM_Enabler{
		Services: make(map[string]*Service),
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
	l.Services[config.SvcName] = InitService(config)

	return nil
}

func (l *LCM_Enabler) Configure_Service(svcName string, config i.ServiceConfig) error {
	if _, ok := l.Services[svcName]; !ok {
		return errors.New("service " + svcName + " not found")
	}
	
	if len(l.Services[svcName].Policies) != 0 {
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

func (l *LCM_Enabler) Policy_Information(svcName string, policyName string) (any, error) {
	if _, ok := l.Services[svcName]; !ok {
		return nil, errors.New("service " + svcName + " not found")
	}

	if _, ok := l.Services[svcName].Policies[policyName]; !ok {
		return nil, errors.New("policy " + policyName + " not found")
	}

	var pConfig i.PolicyConfig = i.PolicyConfig{}
	for _, config := range l.Services[svcName].Config.Policy {
		if config.PolicyName == policyName {
			pConfig = config
			break
		}
	}
	if pConfig.PolicyName == "" {
		return nil, errors.New("policy " + policyName + " not found")
	}

	config, err := u.SendMapData(pConfig)
	if err != nil {
		return nil, err
	}

	return config, nil
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

func (l *LCM_Enabler) DataPacket_Information(svcName string, policyName string, nodeName string, port int) (any, error) {
	if _, ok := l.Services[svcName]; !ok {
		return nil, errors.New("service " + svcName + " not found")
	}

	if _, ok := l.Services[svcName].Policies[policyName]; !ok {
		return nil, errors.New("policy " + policyName + " not found")
	}

	packet_info, err := l.Services[svcName].Policies[policyName].DataPacketInfo(nodeName, port)
	if err != nil {
		return nil, err
	}

	info, err := u.SendMapData(packet_info)
	if err != nil {
		return nil, err
	}
	
	return info, nil
}

func (l *LCM_Enabler) Configure_DataPacket(svcName string, policyName string, data_packet i.ConfigureDataPacket) error {
	if _, ok := l.Services[svcName]; !ok {
		return errors.New("service " + svcName + " not found")
	}

	if _, ok := l.Services[svcName].Policies[policyName]; ok {
		return errors.New("policy " + policyName + " is running")
	}

	err := l.Services[svcName].SetDataPacket(
		policyName,
		data_packet.NodeName, 
		data_packet.Port, 
		data_packet.TransFormat,
	)
	if err != nil {
		return err
	}

	return nil
}

func (l *LCM_Enabler) Execute_Webhook(svcName string, event_type string, content any) error {
	for _, policy := range l.Services[svcName].Policies {
		for _, trigger := range policy.Triggers {
			if trigger.Description.NodeType == "Webhook" && policy.Description.PolicyType == event_type {
				policy.WebhookChMap[trigger.Description.NodeName] <- content
			}
		}
	}
	return nil
}