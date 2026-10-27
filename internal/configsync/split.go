package configsync

import (
	"bytes"
	"errors"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/ACaccel/lcm/internal/model"
)

// Document is one YAML or JSON document together with where it came from.
type Document struct {
	// Path names the file (or "-" for a request body); it ends up in results
	// and in the ownership record.
	Path string
	Data []byte
}

// marker matches a YAML document start or end marker at column 0 with
// nothing but a comment after it.
var marker = regexp.MustCompile(`^(---|\.\.\.)[ \t]*(#.*)?$`)

// SplitDocuments splits a multi-document YAML stream into its documents.
// The split is textual: document markers only count at column 0, which is
// where the YAML specification puts them, so block scalars cannot contain
// one. Splitting this way, instead of decoding the stream, lets a document
// with a syntax error be reported alone while the others are still applied.
// Documents that are empty or hold only comments are dropped.
func SplitDocuments(path string, data []byte) []Document {
	var docs []Document
	var cur [][]byte
	flush := func() {
		if !blank(cur) {
			docs = append(docs, Document{Path: path, Data: bytes.Join(cur, []byte("\n"))})
		}
		cur = nil
	}
	for line := range bytes.SplitSeq(data, []byte("\n")) {
		line = bytes.TrimSuffix(line, []byte("\r"))
		if marker.Match(line) {
			flush()
			continue
		}
		cur = append(cur, line)
	}
	flush()
	return docs
}

func blank(lines [][]byte) bool {
	for _, l := range lines {
		t := bytes.TrimSpace(l)
		if len(t) > 0 && t[0] != '#' {
			return false
		}
	}
	return true
}

// StreamError reports a request body that holds nothing applicable: no
// document at all, or no document that is even valid YAML. Individual bad
// documents among good ones are not an error here; Apply reports them.
func StreamError(docs []Document) error {
	if len(docs) == 0 {
		return model.Issues{{Message: "no documents found"}}
	}
	var first error
	for _, d := range docs {
		var n yaml.Node
		err := yaml.Unmarshal(d.Data, &n)
		if err == nil {
			return nil
		}
		if first == nil {
			first = err
		}
	}
	return model.Issues{{Message: strings.TrimPrefix(first.Error(), "yaml: ")}}
}

// Head reads the kind and name of a document without validating it, so
// that failures can say which document they belong to.
func Head(data []byte) (kind model.Kind, name string) {
	var head struct {
		Kind     model.Kind `yaml:"kind"`
		Metadata struct {
			Name string `yaml:"name"`
		} `yaml:"metadata"`
	}
	if err := yaml.Unmarshal(data, &head); err != nil {
		return "", ""
	}
	return head.Kind, head.Metadata.Name
}

// ParseKind reads a kind as clients write it: "service", "Services",
// "policies" and so on.
func ParseKind(s string) (model.Kind, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "service", "services", "svc":
		return model.KindService, nil
	case "policy", "policies":
		return model.KindPolicy, nil
	case "module", "modules":
		return model.KindModule, nil
	}
	return "", errors.New("kind must be service, policy or module")
}
