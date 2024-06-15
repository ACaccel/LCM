package utils

import (
	"errors"
	i "lcm/packages/interfaces"

	"github.com/mitchellh/mapstructure"
)

func GetProperty(src any, dest any) error {
	err := mapstructure.Decode(src, &dest)
	if err != nil {
		return err
	}
	return nil
}

// Decode the src (map[string]any) to dest (pointer to struct).
// The field of dest must be exported (capital letter).
// Return error if the the content is nil or the src is not a struct
func DecodeInput(src any, dest any) error {
	err := mapstructure.Decode(src, &dest)
	if err != nil {
		return err
	}
	return nil
}

// Encode the src (struct) to dest (pointer to i.NodeOutput type).
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

func GetRawInputContent(src any) (i.NodeOutput, error) {
	rawOutput, err := src.(map[string]any)
	if !err {
		return nil, errors.New("GetRawInputContent: src must be a map")
	}

	var output i.NodeOutput = make(i.NodeOutput, 1)
	output[0] = rawOutput
	return output, nil
}