// Package templates provides the built-in policy templates: ordinary Policy
// documents (examples/templates) whose spec.params are the knobs of the
// template, plus a descriptor that tells a form how to ask for them.
//
// Instantiating a template fills spec.params, names the policy and, because
// trigger props cannot be expressions, substitutes the parameters that
// triggers use as literals. Everything else keeps reading params.*, so the
// parameters of a deployed policy are changed by editing its document.
package templates

import (
	"fmt"
	"io/fs"
	"regexp"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/ACaccel/lcm/examples"
	"github.com/ACaccel/lcm/internal/model"
	"github.com/ACaccel/lcm/internal/secret"
)

// Annotation records which template a policy came from.
const Annotation = "lcm.dev/template"

// ParamType tells a form which input to show and how to check the value.
type ParamType string

// Parameter types.
const (
	TypeString   ParamType = "string"
	TypeBool     ParamType = "boolean"
	TypeDuration ParamType = "duration"
	// TypeSecret is the name of a secret, never its value.
	TypeSecret ParamType = "secret"
	// TypeService is the name of a service in the catalog.
	TypeService ParamType = "service"
	// TypeStrings is a list of strings.
	TypeStrings ParamType = "strings"
	// TypeJSON is any JSON value; the template says what it expects.
	TypeJSON ParamType = "json"
)

// Param describes one template parameter.
type Param struct {
	Name        string    `json:"name"`
	Type        ParamType `json:"type"`
	Description string    `json:"description"`
	// Required parameters have no default and must be given. RequiredIf
	// names a boolean parameter that makes this one required while true.
	Required   bool   `json:"required"`
	RequiredIf string `json:"requiredIf,omitempty"`
	// Default is the value in the template document; absent for required
	// parameters, whose value there is only an example.
	Default any `json:"default,omitempty"`
}

// Template is one built-in policy template.
type Template struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	// ServiceParam names the parameter that selects the target service.
	ServiceParam string  `json:"serviceParam"`
	Params       []Param `json:"params"`

	doc *yaml.Node
}

// Document returns the template as YAML, with its example values.
func (t Template) Document() string {
	out, _ := marshal(t.doc)
	return string(out)
}

// descriptor is the part of a template that is not in its document.
type descriptor struct {
	id, title, description, serviceParam string
	params                               []Param
}

var (
	service = Param{Name: "service", Type: TypeService, Required: true,
		Description: "Service the policy acts on. It needs at least one critical health check, which decides whether it is healthy."}
	notify = Param{Name: "notifySecret", Type: TypeSecret,
		Description: "Name of a secret that holds a Discord webhook URL. Leave empty to send no notifications."}
)

func healthTimeout() Param {
	return Param{Name: "healthTimeout", Type: TypeDuration,
		Description: "How long to wait for the service to become healthy, such as 5m."}
}

var descriptors = []descriptor{
	{
		id: "auto-deploy", title: "Auto deploy", serviceParam: "service",
		description: "Deploys every new commit of a branch once CI has passed and the deploy window is open, " +
			"waits for the service to become healthy and rolls back otherwise. One Discord message is updated through the stages.",
		params: []Param{
			service,
			{Name: "repo", Type: TypeString, Required: true, Description: "Git repository URL or local path that is polled for new commits."},
			{Name: "branch", Type: TypeString, Description: "Branch to watch."},
			{Name: "requireCI", Type: TypeBool, Description: "Deploy only commits whose GitHub Actions checks passed."},
			{Name: "ciRepo", Type: TypeString, RequiredIf: "requireCI", Description: "GitHub repository as owner/name, for the CI check."},
			{Name: "ciWorkflows", Type: TypeStrings, Description: "Workflow files that must have succeeded, such as ci.yml. Empty checks every check run of the commit."},
			{Name: "ciTokenSecret", Type: TypeSecret, Description: "Name of a secret that holds a GitHub token; needed for private repositories."},
			{Name: "ciWait", Type: TypeDuration, Description: "How long to wait for pending checks before giving up."},
			{Name: "windows", Type: TypeJSON, Description: `Deploy windows as a list of {days, start, end}; days is optional. The default allows every time.`},
			{Name: "timezone", Type: TypeString, Description: "IANA time zone of the windows."},
			healthTimeout(),
			notify,
		},
	},
	{
		id: "scheduled-backup", title: "Scheduled backup", serviceParam: "service",
		description: "Runs the service's backup on a cron schedule, retries it with backoff, runs once after downtime, " +
			"optionally confirms that a heartbeat check reported in, and notifies on failure.",
		params: []Param{
			{Name: "service", Type: TypeService, Required: true, Description: "Service whose backup command runs."},
			{Name: "schedule", Type: TypeString, Description: "Five-field cron expression."},
			{Name: "timezone", Type: TypeString, Description: "IANA time zone of the schedule."},
			{Name: "heartbeatCheck", Type: TypeString, Description: "Name of a heartbeat check of the service that the backup job reports to. Empty skips the confirmation."},
			{Name: "heartbeatTimeout", Type: TypeDuration, Description: "How long to wait for the heartbeat after the backup started."},
			notify,
		},
	},
	{
		id: "post-deploy-verify", title: "Post-deploy verification", serviceParam: "service",
		description: "After any deployment of the service waits for it to become healthy and rolls back when it does not. " +
			"Not for services that also use auto-deploy, which does this itself.",
		params: []Param{service, healthTimeout(), notify},
	},
	{
		id: "restart-on-down", title: "Restart when down", serviceParam: "service",
		description: "Restarts the service when its health turns down and reports whether it recovered. " +
			"This simple version has no rate limit; guards for that arrive with the M2 guard nodes.",
		params: []Param{service, healthTimeout(), notify},
	},
}

var catalog = mustLoad()

// List returns all templates ordered by id.
func List() []Template { return slices.Clone(catalog) }

// Get returns the template with the given id.
func Get(id string) (Template, bool) {
	for _, t := range catalog {
		if t.ID == id {
			return t, true
		}
	}
	return Template{}, false
}

func mustLoad() []Template {
	out := make([]Template, 0, len(descriptors))
	for _, d := range descriptors {
		t, err := load(d)
		if err != nil {
			panic(fmt.Sprintf("templates: %s: %v", d.id, err))
		}
		out = append(out, t)
	}
	slices.SortFunc(out, func(a, b Template) int { return strings.Compare(a.ID, b.ID) })
	return out
}

func load(d descriptor) (Template, error) {
	raw, err := fs.ReadFile(examples.Templates, "templates/"+d.id+".yaml")
	if err != nil {
		return Template{}, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return Template{}, err
	}
	params, err := fileParams(&doc)
	if err != nil {
		return Template{}, err
	}
	t := Template{ID: d.id, Title: d.title, Description: d.description, ServiceParam: d.serviceParam, doc: &doc}
	seen := map[string]bool{}
	for _, p := range d.params {
		def, ok := params[p.Name]
		if !ok {
			return Template{}, fmt.Errorf("parameter %s is not in spec.params", p.Name)
		}
		if p.Required || p.RequiredIf != "" {
			def = nil
		}
		p.Default = def
		if !referenced(&doc, p.Name) {
			return Template{}, fmt.Errorf("parameter %s is not used by any node", p.Name)
		}
		seen[p.Name] = true
		t.Params = append(t.Params, p)
	}
	for name := range params {
		if !seen[name] {
			return Template{}, fmt.Errorf("spec.params.%s has no descriptor", name)
		}
	}
	if !seen[d.serviceParam] {
		return Template{}, fmt.Errorf("service parameter %s is not described", d.serviceParam)
	}
	return t, nil
}

// Render returns the policy document for the template, named name, with the
// given parameters on top of the defaults. Problems with the parameters are
// returned as model.Issues with paths such as params.healthTimeout.
func (t Template) Render(name string, values map[string]any) ([]byte, error) {
	merged, err := t.resolve(values)
	if err != nil {
		return nil, err
	}
	doc := cloneNode(t.doc)
	root := doc.Content[0]
	meta := mapping(root, "metadata")
	setScalar(meta, "name", name)
	labels := ensureMapping(meta, "labels")
	if svc, _ := merged[t.ServiceParam].(string); svc != "" {
		setScalar(labels, "service", svc)
	}
	setScalar(ensureMapping(meta, "annotations"), Annotation, t.ID)

	spec := mapping(root, "spec")
	nodes := seq(spec, "nodes")
	used := map[string]bool{}
	for _, n := range nodes.Content {
		typ := scalarOf(mapping(n, "type"))
		if strings.HasPrefix(typ, "trigger.") {
			substitute(mapping(n, "props"), merged)
			continue
		}
		collect(n, used)
	}
	params := &yaml.Node{Kind: yaml.MappingNode}
	for _, p := range t.Params {
		if !used[p.Name] {
			continue
		}
		var v yaml.Node
		if err := v.Encode(merged[p.Name]); err != nil {
			return nil, fmt.Errorf("parameter %s: %w", p.Name, err)
		}
		params.Content = append(params.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: p.Name}, &v)
	}
	setNode(spec, "params", params)
	return marshal(doc)
}

// resolve merges the defaults with values and checks the result.
func (t Template) resolve(values map[string]any) (map[string]any, error) {
	var is model.Issues
	byName := map[string]Param{}
	merged := map[string]any{}
	for _, p := range t.Params {
		byName[p.Name] = p
		merged[p.Name] = p.Default
	}
	for _, p := range t.Params {
		if p.Default == nil && (p.Type == TypeString || p.Type == TypeSecret || p.Type == TypeService || p.Type == TypeDuration) {
			merged[p.Name] = ""
		}
	}
	for _, k := range sortedKeys(values) {
		p, ok := byName[k]
		if !ok {
			is = append(is, model.Issue{Path: "params." + k, Message: "unknown parameter of template " + t.ID})
			continue
		}
		if values[k] == nil {
			continue
		}
		v, err := normalize(p, values[k])
		if err != nil {
			is = append(is, model.Issue{Path: "params." + k, Message: err.Error()})
			continue
		}
		if s, ok := v.(string); ok && s == "" && p.emptyMeansDefault() {
			continue
		}
		merged[k] = v
	}
	for _, p := range t.Params {
		required := p.Required
		if p.RequiredIf != "" {
			on, _ := merged[p.RequiredIf].(bool)
			required = on
		}
		if s, isString := merged[p.Name].(string); required && (merged[p.Name] == nil || isString && s == "") {
			msg := "is required"
			if p.RequiredIf != "" {
				msg = "is required while " + p.RequiredIf + " is true"
			}
			is = append(is, model.Issue{Path: "params." + p.Name, Message: msg})
		}
	}
	if err := is.Err(); err != nil {
		return nil, err
	}
	return merged, nil
}

// emptyMeansDefault reports whether an empty string stands for "not given".
// Secret names and free-form strings with an empty default may be set to
// empty on purpose, for example to switch notifications off.
func (p Param) emptyMeansDefault() bool {
	switch p.Type {
	case TypeDuration, TypeService:
		return true
	case TypeString:
		return p.Default != nil && p.Default != ""
	}
	return false
}

var serviceRe = regexp.MustCompile(`^[a-z]([a-z0-9-]*[a-z0-9])?$`)

// normalize checks v against the parameter's type and returns the value to
// store.
func normalize(p Param, v any) (any, error) {
	switch p.Type {
	case TypeString, TypeSecret, TypeService, TypeDuration:
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("must be a string")
		}
		switch {
		case s == "":
		case p.Type == TypeSecret && !secret.ValidName(s):
			return nil, fmt.Errorf("%q is not a valid secret name", s)
		case p.Type == TypeService && (len(s) > 63 || !serviceRe.MatchString(s)):
			return nil, fmt.Errorf("%q is not a valid service name", s)
		case p.Type == TypeDuration:
			if d, err := time.ParseDuration(s); err != nil || d <= 0 {
				return nil, fmt.Errorf("%q is not a positive duration such as 5m", s)
			}
		}
		return s, nil
	case TypeBool:
		b, ok := v.(bool)
		if !ok {
			return nil, fmt.Errorf("must be true or false")
		}
		return b, nil
	case TypeStrings:
		list, ok := v.([]any)
		if !ok {
			if ss, isStrings := v.([]string); isStrings {
				list = make([]any, len(ss))
				for i, s := range ss {
					list[i] = s
				}
			} else {
				return nil, fmt.Errorf("must be a list of strings")
			}
		}
		for _, e := range list {
			if _, ok := e.(string); !ok {
				return nil, fmt.Errorf("must be a list of strings")
			}
		}
		return list, nil
	default:
		return v, nil
	}
}
