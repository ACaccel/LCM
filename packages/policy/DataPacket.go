package policy

import (
	"errors"
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
    if !packet.isValid() {
        return errors.New("input content is not valid")
    }

    output, err := transformContent(packet.input.content, packet.transFormat)
    if err != nil {
        return err
    }
    packet.output.content = output
    
    return nil
}

// transform the content based on the transformation format. The type of src must be map[string]any
func transformContent(src any, transFormat map[string]string) (map[string]any, error) {
    input := src.(map[string]any)
    output := make(map[string]any)
    for dstField, srcField := range transFormat {
        srcFieldPath := strings.Split(srcField, ".")
        srcFieldValue := getMapFieldValue(input, srcFieldPath)
        if srcFieldValue == nil {
            return nil, errors.New("field " + srcField + " not found in src")
        }
        output[dstField] = srcFieldValue
    }
    return output, nil
}

// get value based on the field path in the map
func getMapFieldValue(m map[string]any, fieldPath []string) any {
    var newMap any = m
    for _, path := range fieldPath {
        tempVal, err := newMap.(map[string]any)[path]
        newMap = tempVal
        if err || tempVal == nil {
            break
        }
    }
    return newMap
}

/*
 * other methods
 */

func (packet *dataPacket) isValid() bool {
    return packet.input.content != nil
}

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
	packet.input.content = content.(map[string]any)
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