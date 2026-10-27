package api_test

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"
)

// spec is the parsed openapi.yaml, used to check every response the
// integration tests receive against the documented status codes and schemas.
type spec struct {
	doc   map[string]any
	paths []specPath
}

type specPath struct {
	template string
	re       *regexp.Regexp
	literals int
	item     map[string]any
}

var (
	specOnce sync.Once
	specVal  *spec
	specErr  error
)

func loadSpec(t *testing.T) *spec {
	t.Helper()
	specOnce.Do(func() {
		raw, err := os.ReadFile("openapi.yaml")
		if err != nil {
			specErr = err
			return
		}
		s := &spec{}
		if specErr = yaml.Unmarshal(raw, &s.doc); specErr != nil {
			return
		}
		param := regexp.MustCompile(`\\\{[^}]+\\\}`)
		for tmpl, item := range s.doc["paths"].(map[string]any) {
			re := "^/api/v1" + param.ReplaceAllString(regexp.QuoteMeta(tmpl), `[^/]+`) + "$"
			s.paths = append(s.paths, specPath{
				template: tmpl, re: regexp.MustCompile(re), literals: strings.Count(tmpl, "{"), item: item.(map[string]any),
			})
		}
		// Literal segments win over parameters (/hooks/github over /hooks/{name}).
		sort.Slice(s.paths, func(i, j int) bool { return s.paths[i].literals < s.paths[j].literals })
		specVal = s
	})
	if specErr != nil {
		t.Fatalf("openapi.yaml: %v", specErr)
	}
	return specVal
}

// checkResponse fails the test when the response is not documented.
func (s *spec) checkResponse(t *testing.T, method, rawPath string, status int, contentType string, body []byte) {
	t.Helper()
	u, err := url.Parse(rawPath)
	if err != nil {
		t.Fatalf("bad path %q: %v", rawPath, err)
	}
	var item map[string]any
	for _, p := range s.paths {
		if p.re.MatchString(u.EscapedPath()) {
			if _, ok := p.item[strings.ToLower(method)]; ok {
				item = p.item
				break
			}
		}
	}
	if item == nil {
		return // unknown routes answer 404/405 from the router
	}
	op := item[strings.ToLower(method)].(map[string]any)
	responses := op["responses"].(map[string]any)
	resp, ok := responses[fmt.Sprint(status)].(map[string]any)
	if !ok {
		t.Errorf("%s %s answered %d, which openapi.yaml does not document for it; body: %s", method, u.Path, status, body)
		return
	}
	resp = s.deref(resp)
	content, _ := resp["content"].(map[string]any)
	media, ok := content["application/json"].(map[string]any)
	if !ok || !strings.HasPrefix(contentType, "application/json") {
		return
	}
	schema, ok := media["schema"].(map[string]any)
	if !ok {
		return
	}
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		t.Errorf("%s %s: response is not JSON: %v", method, u.Path, err)
		return
	}
	for _, e := range s.validate(v, schema, "$") {
		t.Errorf("%s %s (%d): %s", method, u.Path, status, e)
	}
}

func (s *spec) deref(m map[string]any) map[string]any {
	for {
		ref, ok := m["$ref"].(string)
		if !ok {
			return m
		}
		var cur any = s.doc
		for _, part := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
			cur = cur.(map[string]any)[part]
		}
		m = cur.(map[string]any)
	}
}

// branches flattens allOf into the list of object-defining schemas.
func (s *spec) branches(schema map[string]any) []map[string]any {
	schema = s.deref(schema)
	all, ok := schema["allOf"].([]any)
	if !ok {
		return []map[string]any{schema}
	}
	var out []map[string]any
	if _, has := schema["properties"]; has {
		out = append(out, schema)
	}
	for _, b := range all {
		out = append(out, s.branches(b.(map[string]any))...)
	}
	return out
}

// validate checks v against a schema of the subset of JSON Schema that
// openapi.yaml uses. Objects must not carry properties the schema does not
// declare unless it allows additional ones: that is what catches a DTO that
// drifted from the document.
func (s *spec) validate(v any, schema map[string]any, path string) []string {
	schema = s.deref(schema)
	if len(schema) == 0 {
		return nil
	}
	if alts, ok := schema["oneOf"].([]any); ok {
		var first []string
		for i, a := range alts {
			errs := s.validate(v, a.(map[string]any), path)
			if len(errs) == 0 {
				return nil
			}
			if i == 0 {
				first = errs
			}
		}
		return append([]string{path + ": matches none of the oneOf alternatives"}, first...)
	}
	branches := s.branches(schema)
	var errs []string
	if t := typeOf(branches); t != nil && !typeMatches(v, t) {
		return []string{fmt.Sprintf("%s: got %s, want %v", path, kindOf(v), t)}
	}
	for _, b := range branches {
		if enum, ok := b["enum"].([]any); ok && v != nil && !containsValue(enum, v) {
			errs = append(errs, fmt.Sprintf("%s: %v is not one of %v", path, v, enum))
		}
		if f, _ := b["format"].(string); f == "date-time" {
			if str, ok := v.(string); ok {
				if _, err := time.Parse(time.RFC3339Nano, str); err != nil {
					errs = append(errs, fmt.Sprintf("%s: %q is not a date-time", path, str))
				}
			}
		}
	}
	switch x := v.(type) {
	case map[string]any:
		errs = append(errs, s.validateObject(x, branches, path)...)
	case []any:
		for _, b := range branches {
			if items, ok := b["items"].(map[string]any); ok {
				for i, el := range x {
					errs = append(errs, s.validate(el, items, fmt.Sprintf("%s[%d]", path, i))...)
				}
			}
		}
	}
	return errs
}

func (s *spec) validateObject(obj map[string]any, branches []map[string]any, path string) []string {
	var errs []string
	known := map[string]map[string]any{}
	open := false
	hasProps := false
	for _, b := range branches {
		if props, ok := b["properties"].(map[string]any); ok {
			hasProps = true
			for k, p := range props {
				known[k] = p.(map[string]any)
			}
		}
		if ap, ok := b["additionalProperties"]; ok && ap != false {
			open = true
		}
		if req, ok := b["required"].([]any); ok {
			for _, r := range req {
				if _, present := obj[r.(string)]; !present {
					errs = append(errs, fmt.Sprintf("%s: missing required property %q", path, r))
				}
			}
		}
	}
	for k, val := range obj {
		sub, declared := known[k]
		switch {
		case declared:
			errs = append(errs, s.validate(val, sub, path+"."+k)...)
		case open:
			for _, b := range branches {
				if ap, ok := b["additionalProperties"].(map[string]any); ok {
					errs = append(errs, s.validate(val, ap, path+"."+k)...)
				}
			}
		case hasProps:
			errs = append(errs, fmt.Sprintf("%s: property %q is not in the schema", path, k))
		}
	}
	return errs
}

func typeOf(branches []map[string]any) []string {
	for _, b := range branches {
		switch t := b["type"].(type) {
		case string:
			return []string{t}
		case []any:
			out := make([]string, len(t))
			for i, x := range t {
				out[i] = x.(string)
			}
			return out
		}
	}
	return nil
}

func typeMatches(v any, types []string) bool {
	for _, t := range types {
		switch t {
		case "null":
			if v == nil {
				return true
			}
		case "string":
			if _, ok := v.(string); ok {
				return true
			}
		case "boolean":
			if _, ok := v.(bool); ok {
				return true
			}
		case "object":
			if _, ok := v.(map[string]any); ok {
				return true
			}
		case "array":
			if _, ok := v.([]any); ok {
				return true
			}
		case "number":
			if _, ok := v.(float64); ok {
				return true
			}
		case "integer":
			if f, ok := v.(float64); ok && f == float64(int64(f)) {
				return true
			}
		}
	}
	return false
}

func kindOf(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case bool:
		return "boolean"
	case float64:
		return "number"
	case map[string]any:
		return "object"
	case []any:
		return "array"
	}
	return fmt.Sprintf("%T", v)
}

func containsValue(list []any, v any) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
