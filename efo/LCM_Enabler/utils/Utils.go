package utils

import (
	"errors"
	"fmt"
	"io"
	"os"
	// "encoding/json"
	"gopkg.in/yaml.v2"
	jsoniter "github.com/json-iterator/go"
)
var json = jsoniter.ConfigCompatibleWithStandardLibrary

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
		return fmt.Errorf("failed to read YAML file: %w", err)
	}
	defer yamlFile.Close()

	yamlFileContent, err := io.ReadAll(yamlFile)
	if err != nil {
		return fmt.Errorf("failed to read YAML file: %w", err)
	}

	// Keep the yaml error intact: it carries the line number, which is the
	// only actionable detail for a hand-written config.
	err = yaml.Unmarshal(yamlFileContent, content)
	if err != nil {
		return fmt.Errorf("failed to parse YAML file: %w", err)
	}

	return nil
}

func SendMapData(data any) (any, error) {
	byteData, err := json.Marshal(data)
	if err != nil {
		return nil, errors.New("SendMapData error: " + err.Error())
	}
	var jsonData interface{}
	if err := json.Unmarshal(byteData, &jsonData); err != nil {
		return nil, errors.New("SendMapData error: " + err.Error())
	}
	return jsonData, nil
}