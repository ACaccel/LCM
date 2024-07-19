package lcmenabler

import (
	i "lcm/packages/interfaces"
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

func (l *LCM_Enabler) Execute_Webhook(service_name string, event_type string, content any) error {
	for _, policy := range l.Services[service_name].Policies {
		for _, trigger := range policy.Triggers {
			if trigger.Description.NodeType == "Webhook" && policy.Description.PolicyType == event_type {
				policy.WebhookChMap[trigger.Description.NodeName] <- content
			}
		}
	}
	return nil
}

func (l *LCM_Enabler) Register_Service(config i.ServiceConfig) error {
	l.Services[config.SvcName] = InitService(config)
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

func (l *LCM_Enabler) Configure_Service(svcName string, config i.ServiceConfig) error {
	err := l.Services[svcName].Configure(config)
	if err != nil {
		return err
	}
	return nil
}