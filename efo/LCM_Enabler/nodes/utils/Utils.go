package utils

import (
	i "1935/efo/LCM_Enabler/interfaces"
	"errors"
	"unicode"

	"github.com/fatih/structs"
	"github.com/mitchellh/mapstructure"
)

// Get the node's property from src to dest (pointer to struct).
// The field of dest must be exported (capital letter).
func GetProperty(src any, dest any) error {
	for _, field := range structs.Fields(dest) {
		r := rune(field.Name()[0])
		if unicode.IsLower(r) {
			return errors.New("LCM: property field name must be exported (capitalized)")
		}
	}

	cfg := &mapstructure.DecoderConfig{
		TagName: "yaml",
		Result:  dest,
	}
	decoder, err := mapstructure.NewDecoder(cfg)
	if err != nil {
		return err
	}
	return decoder.Decode(src)
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
// The field of src must be exported (capital letter).
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
