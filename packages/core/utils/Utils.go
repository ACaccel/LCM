package utils

import (
	"encoding/json"
	"errors"
	"gopkg.in/yaml.v2"
	"io"
	"os"

	i "interfaces"
	p "policy"
)

func ReadJson(path string, content *[]i.PolicyConfig) error {
	jsonFile, err := os.Open(path)
	if err != nil {
		return errors.New("failed to parse JSON file")
	}
	defer jsonFile.Close()

	jsonFileContent, err := io.ReadAll(jsonFile)
	if err != nil {
		return errors.New("failed to parse JSON file")
	}

	// var content any
	err = json.Unmarshal(jsonFileContent, &content)
	if err != nil {
		return errors.New("failed to parse JSON file")
	}

	return nil
}

func WriteJson(path string, content any) error {
	jsonFile, err := os.Open(path)
	if err != nil {
		return errors.New("failed to parse JSON file")
	}
	defer jsonFile.Close()

	jsonFileContent, err := json.Marshal(content)
	if err != nil {
		return errors.New("failed to parse JSON file")
	}

	err = os.WriteFile(path, jsonFileContent, 0644)
	if err != nil {
		return errors.New("failed to parse JSON file")
	}

	return nil
}

func ReadYaml(path string, content any) error {
	yamlFile, err := os.Open(path)
	if err != nil {
		return errors.New("failed to parse YAML file")
	}
	defer yamlFile.Close()

	yamlFileContent, err := io.ReadAll(yamlFile)
	if err != nil {
		return errors.New("failed to parse YAML file")
	}

	err = yaml.Unmarshal(yamlFileContent, content)
	if err != nil {
		return errors.New("failed to parse YAML file")
	}

	return nil
}

func DeployPolicy(configPath string, policyName string, corePolicies []p.Policy) (p.Policy, error) {
	// check if policy already deployed
	for _, deployedPolicies := range corePolicies {
		if deployedPolicies.Description.PolicyName == policyName {
			return p.Policy{}, errors.New("policy " + policyName + " already deployed")
		}
	}

	var config i.ConfigFormat
	err := ReadYaml(configPath, &config)
	
	if err != nil {
		return p.Policy{}, errors.New("failed to parse JSON file")
	}

	// check if policy exists in database
	var policyConfig i.PolicyConfig = i.PolicyConfig{}
	for _, config := range config.Policy {
		if config.PolicyName == policyName {
			policyConfig = config
			break
		}
	}
	if policyConfig.PolicyName == "" {
		return p.Policy{}, errors.New("policy " + policyName + " not found")
	}

	var newPolicy p.Policy = p.Policy{}
	newPolicy.Init(policyConfig)
	return newPolicy, nil
}