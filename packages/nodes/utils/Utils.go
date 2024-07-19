package utils

import (
	"errors"
	i "lcm/packages/interfaces"

	"github.com/mitchellh/mapstructure"
	"github.com/fatih/structs"
)

// Get the node's property from src to dest (pointer to struct).
// The field of dest must be exported (capital letter).
func GetProperty(src any, dest any) error {
	err := mapstructure.Decode(src, &dest)
	if err != nil {
		return err
	}
	return nil
}

// Decode src (map[string]any) to dest (pointer to struct).
// The field of dest must be exported (capital letter).
func DecodeInput(src any, dest any) error {
	err := mapstructure.Decode(src, &dest)
	if err != nil {
		return err
	}
	return nil
}

// Encode src (struct) to dest (i.NodeOutput type).
func EncodeOutput(src any, desc i.NodeDescription) (i.NodeOutput, error) {
	var encoded_output i.NodeOutput = make(i.NodeOutput, desc.NumOutputPort)
	var output map[string]any = make(map[string]any)
	err := mapstructure.Decode(src, &output)
	if err != nil {
		return nil, err
	}

	for i := 0; i < desc.NumOutputPort; i++ {
		encoded_output[i] = output
	}
	return encoded_output, nil
}

// Encode src (struct) to dest (i.NodeOutput type).
// Only used in Trigger nodes.
func EncodeTriggerOutput(src any) (i.NodeOutput, error) {
	mapOutput := structs.Map(src)
	output, err := EncodeMapNodeOutput(mapOutput)
	if err != nil {
		return nil, err
	}
	return output, nil
}

// Encode src (map) to dest (i.NodeOutput type).
// Only used in Trigger nodes.
func EncodeMapNodeOutput(src any) (i.NodeOutput, error) {
	rawOutput, err := src.(map[string]any)
	if !err {
		return nil, errors.New("EncodeMapNodeOutput: src must be a map")
	}

	var output i.NodeOutput = make(i.NodeOutput, 1)
	output[0] = rawOutput
	return output, nil
}

func OperatorExec(operator string, a float64, b float64) bool {
	switch operator {
	case "==":
		return a == b
	case "!=":
		return a != b
	case ">":
		return a > b
	case "<":
		return a < b
	case ">=":
		return a >= b
	case "<=":
		return a <= b
	default:
		return false
	}
}