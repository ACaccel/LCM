package utils

import (
	"encoding/json"
	"errors"
	"io"
	"os"

	"gopkg.in/yaml.v2"

	i "lcm/packages/interfaces"
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