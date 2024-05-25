package utils

import (
	"errors"
	// "fmt"
	// "reflect"

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

// Encode the src (struct) to dest (pointer to map[string]any).
func EncodeOutput(src any, dest any) error {
	err := mapstructure.Decode(src, &dest)
	if err != nil {
		return err
	}
	return nil
}

func GetRawInputContent(src any) (any, error) {
	m, err := src.(map[string]any)
	if !err {
		return nil, errors.New("GetRawInputContent: src must be a map")
	}
	return m, nil
}