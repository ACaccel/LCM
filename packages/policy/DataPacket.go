package policy

import (
	"errors"
	"fmt"
	"strings"
	i "lcm/packages/interfaces"
)

type dataPacket struct {
	i.DataPacketInfo

	ready   		chan bool
}

func newDataPacket(nodeName string, port int) *dataPacket {
	packet := dataPacket{}
	packet.Name = fmt.Sprintf("%s-output-%d", nodeName, port)
	packet.Input.InPort = -1
	packet.Input.Content = nil
	packet.Output.OutPort = -1
	packet.Output.Content = nil
	packet.TransFormat = nil
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

    output, err := transformContent(packet.Input.Content, packet.TransFormat)
    if err != nil {
        return err
    }
    packet.Output.Content = output
    
    return nil
}

// transform the content based on the transformation format.
func transformContent(src i.DataPacketContent, transFormat i.TransFormat) (i.DataPacketContent, error) {
    input := src
    output := make(map[string]any)
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
    return packet.Input.Content != nil
}

func (packet *dataPacket) clean() {
	packet.Input.Content = nil
	packet.Output.Content = nil
	close(packet.ready)
	packet.ready = make(chan bool, 1)
}

func (packet *dataPacket) setInPort(port int) {
	packet.Input.InPort = port
}

func (packet *dataPacket) setOutPort(port int) {
	packet.Output.OutPort = port
}

func (packet *dataPacket) outPort() int {
	return packet.Output.OutPort
}

func (packet *dataPacket) setContent(content i.DataPacketContent) {
	packet.Input.Content = content
}

func (packet *dataPacket) content() i.DataPacketContent {
	return packet.Output.Content
}

func (packet *dataPacket) rawContent() i.DataPacketContent {
	if packet.Input.Content == nil {
		return i.DataPacketContent{}
	}
	return packet.Input.Content
}

func (packet *dataPacket) setTransFormat(transFormat i.TransFormat) {
	packet.TransFormat = transFormat
}

func (packet *dataPacket) setReady(){
	packet.ready <- true
}

func (packet *dataPacket) isReady() bool {
	ready := <-packet.ready
	return ready
}

func (packet *dataPacket) info() i.DataPacketInfo {
	return packet.DataPacketInfo
}