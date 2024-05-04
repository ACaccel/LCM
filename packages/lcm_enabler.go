package lcmenabler

import (
	i "lcm/packages/interfaces"
)

type ILCM_Enabler interface {
	Register_Service()
	Upload_Config()
	Deploy_Policy()
	Undeploy_Policy()
}

type LCM_Enabler struct {
	Services map[string]*Service
}

func (l *LCM_Enabler) Execute_Webhook(webhook i.WebhookEvent) {
	for _, policy := range l.Services[webhook.SvcName].Policies {
		for _, trigger := range policy.Triggers {
			if trigger.NodeType == "Webhook" {
				headNode := policy.PolicyList.NodeSet[trigger.NodeName]
				policy.Execute(headNode, webhook.Content)
			}
		}
	}
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
		// check if policy already deployed
		err := l.Services[svcName].Undeploy(policyName)
		if err != nil {
			return err
		}
	}
	return nil
}