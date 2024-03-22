package policy

import (
	"errors"
	"reflect"
	"strings"
)

type dataPacket struct {
	input	struct {
		inPort		int
		content		any
	}
	output	struct {
		outPort		int
		content		any
	}
	transFormat		map[string]string
	ready   		bool
}

func newDataPacket() *dataPacket {
	packet := dataPacket{}
	packet.input.inPort = -1
	packet.input.content = nil
	packet.output.outPort = -1
	packet.output.content = nil
	packet.transFormat = nil
	packet.ready = false
	return &packet
}

/*
 * transformer
 */

func (packet *dataPacket) transform() error {
    srcVal := reflect.ValueOf(packet.input.content)
    dstVal, err := transformDst(srcVal, packet.transFormat)
    if err != nil {
        return err
    }

    packet.output.content = dstVal.Interface()
    return nil
}

func transformDst(srcVal reflect.Value, fieldMapping map[string]string) (reflect.Value, error) {
    // build dst struct
    fields, err := buildStruct(fieldMapping)
    if err != nil {
        return reflect.Value{}, err
    }

    dstType := reflect.StructOf(fields)
    dstVal := reflect.New(dstType).Elem()

    // set value for each field
    for dstField, srcField := range fieldMapping {
        srcFieldParts := strings.Split(srcField, ".")
        srcFieldValue := getField(srcVal, srcFieldParts)
        if !srcFieldValue.IsValid() {
            return reflect.Value{}, errors.New("field " + srcField + " not found in src")
        }
        // dstFieldParts := strings.Split(dstField, ".")
        // dstFieldVal := getField(dstVal, dstFieldParts)
        dstFieldVal := dstVal.FieldByName(dstField)
        if !dstFieldVal.IsValid() {
            return reflect.Value{}, errors.New("field " + dstField + " not found in dst")
        }
        if !dstFieldVal.CanSet() {
            return reflect.Value{}, errors.New("field " + dstField + " cannot be set")
        }
        if dstFieldVal.Type() != srcFieldValue.Type() {
            srcFieldValue = srcFieldValue.Convert(dstFieldVal.Type())
            dstFieldVal.Set(srcFieldValue)
        } else {
            dstFieldVal.Set(srcFieldValue)
        }
    }
    return dstVal, nil
}

func buildStruct(fieldMapping map[string]string) ([]reflect.StructField, error) {
    fields := make([]reflect.StructField, 0)
    for dstField := range fieldMapping {
        fieldParts := strings.Split(dstField, ".")
        if len(fieldParts) == 1 {
            fields = append(fields, reflect.StructField{
                Name: fieldParts[0],
                Type: reflect.TypeOf(0),
            })
        } else {
            return nil, errors.New("nested struct not supported yet")
        }
    }
    
    return fields, nil
}

func getField(val reflect.Value, fieldParts []string) reflect.Value {
    for _, part := range fieldParts {
        tempVal := val.FieldByName(part)
        if !tempVal.IsValid() {
            break
        }
        val = tempVal
    }
    return val
}

/*
 * other methods
 */

func (packet *dataPacket) clean() {
	packet.input.content = nil
	packet.output.content = nil
	packet.ready = false
}

func (packet *dataPacket) setInPort(port int) {
	packet.input.inPort = port
}

func (packet *dataPacket) setOutPort(port int) {
	packet.output.outPort = port
}

func (packet *dataPacket) outPort() int {
	return packet.output.outPort
}

func (packet *dataPacket) setContent(content any) {
	packet.input.content = content
}

func (packet *dataPacket) content() any {
	return packet.output.content
}

func (packet *dataPacket) rawContent() any {
	return packet.input.content
}

func (packet *dataPacket) setTransFormat(transFormat map[string]string) {
	packet.transFormat = transFormat
}

func (packet *dataPacket) setReady(ready bool){
	packet.ready = ready
}

func (packet *dataPacket) isReady() bool {
	return packet.ready
}