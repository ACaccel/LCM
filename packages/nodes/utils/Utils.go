package utils

import (
	"errors"
	"reflect"
	
	"github.com/mitchellh/mapstructure"
)

func GetProperty(src any, dest any) {
	mapstructure.Decode(src, &dest)
}

func GetInputContent(src any, dest any) error {
	srcValue := reflect.ValueOf(src)
	destValue := reflect.ValueOf(dest)
	
	var conversionError error = nil
	defer func() {
		if err := recover(); err != nil {
			conversionError = errors.New("GetInputContent: destination is not addressable")
			println(conversionError.Error())
		}
	}()

	if srcValue.Kind() != reflect.Struct || destValue.Kind() != reflect.Pointer {
		return errors.New("GetInputContent: src must be a struct and dest must be a pointer to structs")
	}

	for i := 0; i < srcValue.NumField(); i++ {

		srcField := srcValue.Field(i)
		fieldName := srcValue.Type().Field(i).Name

		destField := destValue.Elem().FieldByName(fieldName)
		if !destField.IsValid() {
			continue
		}

		if destField.CanSet() {
			if srcField.Type() != destField.Type() {
				srcField = srcField.Convert(destField.Type())
				destField.Set(srcField)
			} else {
				destField.Set(srcField)
			}
		} else {
			return errors.New("GetInputContent: destination is not assignable")
		}
	}

	return conversionError
}

func GetRawInputContent(src any) (any, error) {
	m, err := src.(map[string]any)
	if !err {
		return nil, errors.New("GetRawInputContent: src must be a map")
	}
	
	var fields []reflect.StructField

	for key, value := range m {
		field := reflect.StructField{
			Name: key,
			Type: reflect.TypeOf(value),
		}
		fields = append(fields, field)
	}

	structType := reflect.StructOf(fields)
	structValue := reflect.New(structType).Elem()

	for key, value := range m {
		structValue.FieldByName(key).Set(reflect.ValueOf(value))
	}

	return structValue.Interface(), nil
}