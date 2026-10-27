/*
Package HTTPRequest calls an arbitrary HTTP endpoint.

Every other action node here targets a fixed control-plane route. This one takes
the method, URL and body from its properties, which is what a policy needs in
order to talk to anything the testbed does not already expose as a node.
*/
package HTTPRequest

import (
	i "1935/efo/Rule_Framework/interfaces"
	h "1935/efo/Rule_Framework/nodes/Functions/httpnode"
	u "1935/efo/Rule_Framework/nodes/utils"
	"bytes"
	"fmt"
	"net/http"
)

// Header is one outgoing request header.
type Header struct {
	Name  string `yaml:"name"`
	Value string `yaml:"value"`
}

type Property struct {
	Method      string   `yaml:"method"`
	URL         string   `yaml:"url"`
	Body        string   `yaml:"body"`
	ContentType string   `yaml:"content_type"`
	Headers     []Header `yaml:"headers"`
}

// OutputFormat matches the shape every IEEE1935_* action node emits, so the two
// are interchangeable downstream.
type OutputFormat struct {
	Status_code int
	Response    string
}

var Description = i.NodeDescription{
	NodeType:      "HTTPRequest",
	NumInputPort:  1,
	NumOutputPort: 1,
}

type HTTPRequest struct {
	i.FunctionNode
}

func (n *HTTPRequest) Execute(input []any, info i.ExecInfo) i.NodeExecResult {
	var property Property
	u.DecodeProperty(Description.NodeType, n.Config, input, &property)

	result := send(property)

	output := OutputFormat{Status_code: result.StatusCode, Response: result.Body}
	encoded, err := u.EncodeOutput(output, Description)
	if err != nil {
		fmt.Println("HTTPRequest: Encode output error,", err)
	}

	return i.NodeExecResult{Output: encoded}
}

func send(property Property) h.Result {
	if property.URL == "" {
		return h.Result{StatusCode: 400, Body: "HTTPRequest: url is required"}
	}
	method := property.Method
	if method == "" {
		method = http.MethodGet
	}

	// A nil body and an empty *bytes.Reader are not the same to net/http: the
	// latter sets Content-Length: 0, which some servers reject on GET.
	var body *bytes.Reader
	if property.Body != "" {
		body = bytes.NewReader([]byte(property.Body))
	}
	var req *http.Request
	var err error
	if body == nil {
		req, err = http.NewRequest(method, property.URL, nil)
	} else {
		req, err = http.NewRequest(method, property.URL, body)
	}
	if err != nil {
		return h.Result{StatusCode: 500, Body: fmt.Sprintf("Error: %v", err)}
	}

	if property.ContentType != "" {
		req.Header.Set("Content-Type", property.ContentType)
	}
	headers := make(map[string]string, len(property.Headers))
	for _, header := range property.Headers {
		headers[header.Name] = header.Value
	}
	h.WithHeaders(req, headers)

	return h.DoRequest(req)
}
