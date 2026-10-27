package api

import (
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// loadOpenAPI parses the embedded document.
func loadOpenAPI(t *testing.T) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := yaml.Unmarshal(openAPIDocument, &doc); err != nil {
		t.Fatalf("openapi.yaml does not parse: %v", err)
	}
	return doc
}

var httpMethods = []string{"get", "put", "post", "delete", "patch", "head", "options"}

// documentedOperations returns "METHOD /api/v1/path" for every operation.
func documentedOperations(t *testing.T, doc map[string]any) []string {
	t.Helper()
	servers, _ := doc["servers"].([]any)
	if len(servers) != 1 || servers[0].(map[string]any)["url"] != "/api/v1" {
		t.Fatalf("servers = %v, want the single base /api/v1", servers)
	}
	var out []string
	for path, item := range doc["paths"].(map[string]any) {
		for method := range item.(map[string]any) {
			if slices.Contains(httpMethods, method) {
				out = append(out, strings.ToUpper(method)+" /api/v1"+path)
			}
		}
	}
	sort.Strings(out)
	return out
}

func TestOpenAPIMatchesRoutes(t *testing.T) {
	doc := loadOpenAPI(t)
	documented := documentedOperations(t, doc)
	registered := newEnv(t).srv.Routes()

	for _, r := range registered {
		if !slices.Contains(documented, r) {
			t.Errorf("route %q is not in openapi.yaml", r)
		}
	}
	for _, d := range documented {
		if !slices.Contains(registered, d) {
			t.Errorf("openapi.yaml documents %q, which is not a registered route", d)
		}
	}
}

func TestOpenAPIReferencesResolve(t *testing.T) {
	doc := loadOpenAPI(t)
	var walk func(path string, v any)
	walk = func(path string, v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, child := range x {
				if k == "$ref" {
					ref, _ := child.(string)
					if !resolves(doc, ref) {
						t.Errorf("%s: unresolved $ref %q", path, ref)
					}
					continue
				}
				walk(path+"/"+k, child)
			}
		case []any:
			for i, child := range x {
				walk(path+"/"+strconv.Itoa(i), child)
			}
		}
	}
	walk("", doc)
}

func resolves(doc map[string]any, ref string) bool {
	rest, ok := strings.CutPrefix(ref, "#/")
	if !ok {
		return false
	}
	var cur any = doc
	for _, part := range strings.Split(rest, "/") {
		m, ok := cur.(map[string]any)
		if !ok {
			return false
		}
		if cur, ok = m[part]; !ok {
			return false
		}
	}
	return true
}

var pathParam = regexp.MustCompile(`\{([^}]+)\}`)

func TestOpenAPIOperationsAreWellFormed(t *testing.T) {
	doc := loadOpenAPI(t)
	components := doc["components"].(map[string]any)
	sharedParams := components["parameters"].(map[string]any)

	// paramNames lists the declared parameter names with their location.
	paramNames := func(list any) map[string]bool {
		out := map[string]bool{}
		items, _ := list.([]any)
		for _, p := range items {
			m := p.(map[string]any)
			if ref, ok := m["$ref"].(string); ok {
				m = sharedParams[strings.TrimPrefix(ref, "#/components/parameters/")].(map[string]any)
			}
			out[m["in"].(string)+":"+m["name"].(string)] = true
		}
		return out
	}

	seenIDs := map[string]string{}
	for path, item := range doc["paths"].(map[string]any) {
		pathLevel := paramNames(item.(map[string]any)["parameters"])
		for method, op := range item.(map[string]any) {
			if !slices.Contains(httpMethods, method) {
				continue
			}
			o := op.(map[string]any)
			where := strings.ToUpper(method) + " " + path
			id, _ := o["operationId"].(string)
			if id == "" {
				t.Errorf("%s: no operationId", where)
			} else if prev, dup := seenIDs[id]; dup {
				t.Errorf("%s: operationId %q already used by %s", where, id, prev)
			}
			seenIDs[id] = where
			if _, ok := o["responses"].(map[string]any); !ok {
				t.Errorf("%s: no responses", where)
			}
			if tags, _ := o["tags"].([]any); len(tags) == 0 {
				t.Errorf("%s: no tags", where)
			}
			declared := paramNames(o["parameters"])
			for _, m := range pathParam.FindAllStringSubmatch(path, -1) {
				key := "path:" + m[1]
				if !declared[key] && !pathLevel[key] {
					t.Errorf("%s: path parameter %q is not declared", where, m[1])
				}
			}
		}
	}
}

func TestOpenAPIServed(t *testing.T) {
	e := newEnv(t)
	w := e.do("GET", "/api/v1/openapi.yaml", nil)
	wantStatus(t, w, 200)
	if ct := w.Header().Get("Content-Type"); ct != "application/yaml" {
		t.Errorf("Content-Type = %q", ct)
	}
	if !strings.HasPrefix(w.Body.String(), "openapi: 3.1.0") {
		t.Errorf("body starts with %q", w.Body.String()[:20])
	}
}
