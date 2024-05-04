package utils

import (
	"errors"
	"io"
	"os"
	"encoding/json"
	"gopkg.in/yaml.v2"
)

func ReadJson(path string, content any) error {
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