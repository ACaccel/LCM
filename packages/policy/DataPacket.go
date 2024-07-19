package policy

import (
	"errors"
	i "lcm/packages/interfaces"
	"strings"
)

type dataPacket struct {
	name 			string
	input	struct {
		inPort		int
		content		i.DataPacketContent
	}
	output	struct {
		outPort		int
		content		i.DataPacketContent
	}
	transFormat		i.TransFormat
	ready   		chan bool
}

func newDataPacket(name string) *dataPacket {
	packet := dataPacket{}
	packet.name = name
	packet.input.inPort = -1
	packet.input.content = nil
	packet.output.outPort = -1
	packet.output.content = nil
	packet.transFormat = nil
	packet.ready = make(chan bool, 1)
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

// transform the content based on the transformation format.
func transformContent(src i.DataPacketContent, transFormat i.TransFormat) (i.DataPacketContent, error) {
    input := src
    output := make(map[string]any)
    // for dstField, srcField := range transFormat {
    //     srcFieldPath := strings.Split(srcField, ".")
    //     srcFieldValue := getMapFieldValue(input, srcFieldPath)
    //     if srcFieldValue == nil {
    //         return nil, errors.New("field " + srcField + " not found in src")
    //     }
    //     output[dstField] = srcFieldValue
    // }
	for _, format := range transFormat {
		srcFieldPath := strings.Split(format.Src, ".")
		srcFieldValue := getMapFieldValue(input, srcFieldPath)
		if srcFieldValue == nil {
			return nil, errors.New("field " + format.Src + " not found in src")
		}
		output[format.Dst] = srcFieldValue
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
	close(packet.ready)
	packet.ready = make(chan bool, 1)
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

func (packet *dataPacket) setContent(content i.DataPacketContent) {
	packet.input.content = content
}

func (packet *dataPacket) content() i.DataPacketContent {
	return packet.output.content
}

func (packet *dataPacket) rawContent() i.DataPacketContent {
	if packet.input.content == nil {
		return i.DataPacketContent{}
	}
	return packet.input.content
}

func (packet *dataPacket) setTransFormat(transFormat i.TransFormat) {
	packet.transFormat = transFormat
}

func (packet *dataPacket) setReady(){
	packet.ready <- true
}

func (packet *dataPacket) isReady() bool {
	ready := <-packet.ready
	return ready
}