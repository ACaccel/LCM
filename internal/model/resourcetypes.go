package model

import "slices"

// Resource types.
const (
	ResourceService = "service"
	ResourceMachine = "machine"
	ResourceRouter  = "router"
)

// Adapters that are not service runtimes.
const (
	AdapterLocal  = "local"
	AdapterXiaomi = "xiaomi"
)

// ResourceConfig is the typed config of a resource: *ServiceConfig,
// *MachineConfig or *RouterConfig, chosen by the resource type. The set is
// closed; new types are added to the registry below.
type ResourceConfig interface {
	resourceConfig()
	normalize(adapter string)
	validate(adapter, base string, is *Issues)
}

// ResourceTypeInfo describes a resource type for clients such as the web
// console, which builds its forms from it instead of repeating rules.
type ResourceTypeInfo struct {
	Type            string   `json:"type"`
	Adapters        []string `json:"adapters"`
	NeedsConnection bool     `json:"needsConnection"`
	Description     string   `json:"description"`
}

type resourceType struct {
	ResourceTypeInfo
	codec configCodec
}

// resourceTypes is the registry of resource types in display order.
var resourceTypes = []resourceType{
	{
		ResourceTypeInfo: ResourceTypeInfo{
			Type: ResourceService,
			Adapters: []string{
				string(RuntimeSystemd), string(RuntimeDocker), string(RuntimeCompose),
				string(RuntimeCommand), string(RuntimeExternal),
			},
			Description: "A process on this host controlled through a runtime adapter, with optional source, backup and jobs.",
		},
		codec: codec[ServiceConfig, *ServiceConfig]{},
	},
	{
		ResourceTypeInfo: ResourceTypeInfo{
			Type:        ResourceMachine,
			Adapters:    []string{AdapterLocal},
			Description: "The local Linux host LCM runs on: bounded native observation, maintenance and power operations.",
		},
		codec: codec[MachineConfig, *MachineConfig]{},
	},
	{
		ResourceTypeInfo: ResourceTypeInfo{
			Type:            ResourceRouter,
			Adapters:        []string{AdapterXiaomi},
			NeedsConnection: true,
			Description:     "A network router reached through a host-configured connection: observation and controlled reboot.",
		},
		codec: codec[RouterConfig, *RouterConfig]{},
	},
}

// ResourceTypes returns the descriptors of every resource type.
func ResourceTypes() []ResourceTypeInfo {
	out := make([]ResourceTypeInfo, len(resourceTypes))
	for i, t := range resourceTypes {
		out[i] = t.ResourceTypeInfo
		out[i].Adapters = slices.Clone(t.Adapters)
	}
	return out
}

func lookupType(typ string) (resourceType, bool) {
	for _, t := range resourceTypes {
		if t.Type == typ {
			return t, true
		}
	}
	return resourceType{}, false
}

func typeNames() []string {
	out := make([]string, len(resourceTypes))
	for i, t := range resourceTypes {
		out[i] = t.Type
	}
	return out
}

// configCodec decodes documents whose config has one concrete type, so that
// unknown config fields are rejected by the same strict decoder (with the
// same line numbers) as the rest of the document.
type configCodec interface {
	decodeDocument(data []byte) (*Resource, error)
	decodeSpec(data []byte) (ResourceSpec, error)
}

type codec[C any, P interface {
	*C
	ResourceConfig
}] struct{}

type typedSpec[C any] struct {
	Type       string     `yaml:"type"`
	Adapter    string     `yaml:"adapter"`
	Connection string     `yaml:"connection,omitempty"`
	Config     C          `yaml:"config,omitempty"`
	Checks     []Check    `yaml:"checks,omitempty"`
	Relations  []Relation `yaml:"relations,omitempty"`
}

func (s *typedSpec[C]) spec(cfg ResourceConfig) ResourceSpec {
	return ResourceSpec{Type: s.Type, Adapter: s.Adapter, Connection: s.Connection, Config: cfg, Checks: s.Checks, Relations: s.Relations}
}

func (codec[C, P]) decodeDocument(data []byte) (*Resource, error) {
	var doc struct {
		Header `yaml:",inline"`
		Spec   typedSpec[C] `yaml:"spec"`
	}
	if err := decodeOne(data, &doc, true); err != nil {
		return nil, err
	}
	return &Resource{Header: doc.Header, Spec: doc.Spec.spec(P(&doc.Spec.Config))}, nil
}

func (codec[C, P]) decodeSpec(data []byte) (ResourceSpec, error) {
	var s typedSpec[C]
	if err := decodeOne(data, &s, true); err != nil {
		return ResourceSpec{}, err
	}
	return s.spec(P(&s.Config)), nil
}

// unknownConfig holds the config of a document whose type is not known, so
// that the rest of the document can still be checked and reported at once.
type unknownConfig map[string]any

func (*unknownConfig) resourceConfig()                  {}
func (*unknownConfig) normalize(string)                 {}
func (*unknownConfig) validate(string, string, *Issues) {}
