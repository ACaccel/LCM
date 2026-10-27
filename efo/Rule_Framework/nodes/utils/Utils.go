package utils

import (
	i "1935/efo/LCM_Enabler/interfaces"
	abi "1935/efo/Rule_Framework/interfaces"
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"unicode"

	"github.com/fatih/structs"
	"github.com/mitchellh/mapstructure"
)

// RunContext returns a context that is cancelled when the run this node call
// belongs to is over (ExecInfo.RunDone). A node doing slow HTTP work should
// build its requests on it, so an in-flight call is abandoned instead of
// delaying the next trigger event or an undeploy. The returned cancel must be
// deferred: it also releases the internal watcher goroutine.
func RunContext(info abi.ExecInfo) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	runDone := info.RunDone
	go func() {
		select {
		case <-runDone:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}

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

// DecodeProperty is the one property-decode entry point for function nodes: it
// decodes cfg.Property into dest and, when cfg.FillPropertyFromInput is set,
// fills the fields the static config left empty from the first input packet.
// Failures are logged under nodeType and do not abort; the node runs with
// whatever decoded, which is the behavior every node already had.
func DecodeProperty(nodeType string, cfg i.NodeConfig, input []any, dest any) {
	if err := GetProperty(cfg.Property, dest); err != nil {
		fmt.Println(nodeType+": Get property error,", err)
	}
	if cfg.FillPropertyFromInput {
		if ferr := FillPropertyFromInput(FirstInputPacket(input), dest); ferr != nil {
			fmt.Println(nodeType+": fill property from input error,", ferr)
		}
	}
}

// FirstInputPacket returns input[0] when present, otherwise nil.
func FirstInputPacket(input []any) any {
	if len(input) == 0 {
		return nil
	}
	return input[0]
}

// FillPropertyFromInput merges the first input DataPacket into dest (pointer to struct).
// Call only when NodeConfig.FillPropertyFromInput is true; if the flag is false or omitted,
// do not call—only Property should be used.
//
// Only keys that already exist on dest (via yaml struct tags) are considered. A field is
// replaced only if it is still "empty": nil, empty string, or empty slice/map. Numeric zero
// and bool false are not treated as empty (avoid ambiguity with valid config).
func FillPropertyFromInput(packet any, dest any) error {
	if packet == nil || dest == nil {
		return nil
	}
	propMap, err := structToYAMLKeyMap(dest)
	if err != nil {
		return err
	}
	var inMap map[string]any
	if err := mapstructure.Decode(packet, &inMap); err != nil {
		return err
	}
	for k, pv := range propMap {
		if isMergeEmpty(pv) {
			if iv, ok := inMap[k]; ok && !isMergeEmpty(iv) {
				propMap[k] = iv
			}
		}
	}
	cfg := &mapstructure.DecoderConfig{
		TagName:          "yaml",
		Result:           dest,
		WeaklyTypedInput: true,
		ZeroFields:       false,
	}
	decoder, err := mapstructure.NewDecoder(cfg)
	if err != nil {
		return err
	}
	return decoder.Decode(propMap)
}

func structToYAMLKeyMap(dest any) (map[string]any, error) {
	rv := reflect.ValueOf(dest)
	if rv.Kind() != reflect.Ptr || rv.IsNil() {
		return nil, errors.New("FillPropertyFromInput: dest must be non-nil pointer to struct")
	}
	rv = rv.Elem()
	if rv.Kind() != reflect.Struct {
		return nil, errors.New("FillPropertyFromInput: dest must be pointer to struct")
	}
	rt := rv.Type()
	out := make(map[string]any)
	for i := 0; i < rt.NumField(); i++ {
		sf := rt.Field(i)
		if sf.PkgPath != "" {
			continue
		}
		key := sf.Name
		if tag := sf.Tag.Get("yaml"); tag != "" && tag != "-" {
			key = strings.Split(tag, ",")[0]
		}
		out[key] = rv.Field(i).Interface()
	}
	return out, nil
}

func isMergeEmpty(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Ptr, reflect.Interface:
		if rv.IsNil() {
			return true
		}
		return isMergeEmpty(rv.Elem().Interface())
	case reflect.String:
		return rv.Len() == 0
	case reflect.Slice, reflect.Map:
		return rv.Len() == 0
	case reflect.Bool:
		return false
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64:
		return false
	default:
		return false
	}
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
func EncodeOutput(src any, desc abi.NodeDescription) (i.NodeOutput, error) {
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

// ContentOf narrows one input port's value to a content map. A port that never
// arrived holds nil and yields nil, which is the normal state for the ports a
// JoinAny node did not wait for.
func ContentOf(port any) i.DataPacketContent {
	switch typed := port.(type) {
	case i.DataPacketContent:
		return typed
	case map[string]any:
		return typed
	default:
		return nil
	}
}

// EmptyOutput is the output of a node that contributes nothing of its own.
//
// A node that only routes or only waits has nothing to add: what arrived already
// travels on as the namespaces it arrived with, and emitting it again would file
// a second copy of the same values under this node's name -- the same data twice
// on the wire and twice in the execution trace.
//
// Every port gets its own map rather than one shared empty. A consumer with an
// empty transFormat copies what it is handed, and two ports sharing one map
// would alias a single object across both branches and into the trace.
func EmptyOutput(ports int) i.NodeOutput {
	output := make(i.NodeOutput, ports)
	for port := 0; port < ports; port++ {
		output[port] = i.DataPacketContent{}
	}
	return output
}

// FieldValue resolves a condition's field name against one input port.
//
// A bare name is a key of the flattened payload, which is what a condition
// normally wants. A dotted name is resolved against the namespaces instead, so
// "webhook-0.Value" reaches the value the flattening shadowed when a later node
// also emitted Value. The flat view is tried first either way, so a payload that
// genuinely holds a dotted key still works.
func FieldValue(flat i.DataPacketContent, namespaced i.DataPacketContent, field string) (any, bool) {
	if value, found := flat[field]; found {
		return value, true
	}
	owner, key, qualified := strings.Cut(field, ".")
	if !qualified {
		return nil, false
	}
	within := ContentOf(namespaced[owner])
	if within == nil {
		return nil, false
	}
	value, found := within[key]
	return value, found
}

// NumberAt reads a condition's field as a float64.
//
// JSON decodes every number to float64, but a payload built in Go can hold any
// numeric kind, so the common ones are accepted too.
func NumberAt(flat i.DataPacketContent, namespaced i.DataPacketContent, field string) (float64, bool) {
	raw, found := FieldValue(flat, namespaced, field)
	if !found {
		return 0, false
	}
	switch v := raw.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int32:
		return float64(v), true
	case int64:
		return float64(v), true
	default:
		return 0, false
	}
}

// NamespacesAt returns the namespaces that arrived on one input port, or an
// empty set when the caller ran without any -- a trigger, or a module entry.
func NamespacesAt(info abi.ExecInfo, port int) i.DataPacketContent {
	if port < 0 || port >= len(info.Namespaces) || info.Namespaces[port] == nil {
		return i.DataPacketContent{}
	}
	return info.Namespaces[port]
}
