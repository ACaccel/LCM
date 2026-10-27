package httpnode

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	config "1935/efo/Rule_Framework/config"
)

// newClient builds the client every call in this package uses.
//
// The timeout is the point. Without one, an endpoint that accepts a connection
// and then never answers holds the calling walker goroutine for the lifetime of
// the process, and the policy run it belongs to never completes.
func newClient() *http.Client {
	return &http.Client{
		Timeout: time.Duration(config.HTTPTimeoutSeconds() * float64(time.Second)),
	}
}

// WithHeaders sets each header on req, skipping entries with an empty name.
func WithHeaders(req *http.Request, headers map[string]string) {
	for name, value := range headers {
		if name == "" {
			continue
		}
		req.Header.Set(name, value)
	}
}

// Result holds the status code and body text from an HTTP call,
// or an error status and message when the call fails.
type Result struct {
	StatusCode int
	Body       string
}

// UnsupportedRequest returns the standard 400 result for an unrecognised request type.
func UnsupportedRequest() Result {
	return Result{StatusCode: 400, Body: "Unsupported request"}
}

// Do sends an HTTP request and returns the result.
// A transport error or nil response produces a 500 result.
func Do(method, url string, body io.Reader, contentType string) Result {
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return Result{StatusCode: 500, Body: fmt.Sprintf("Error: %v", err)}
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return DoRequest(req)
}

// DoRequest sends a pre-built HTTP request and returns the result.
// Used by multipart cases that need to preserve the "Error creating request" error path.
func DoRequest(req *http.Request) Result {
	res, err := newClient().Do(req)
	if err != nil {
		return Result{StatusCode: 500, Body: fmt.Sprintf("Error: %v", err)}
	}
	if res == nil {
		return Result{StatusCode: 500, Body: "Error: nil response"}
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return Result{StatusCode: res.StatusCode, Body: string(b)}
}

// Get sends a GET request.
func Get(url string) Result {
	return Do(http.MethodGet, url, nil, "")
}

// Delete sends a DELETE request.
func Delete(url string) Result {
	return Do(http.MethodDelete, url, nil, "")
}

// PostJSON marshals payload to JSON and sends a POST request.
func PostJSON(url string, payload any) Result {
	b, err := json.Marshal(payload)
	if err != nil {
		return Result{StatusCode: 500, Body: fmt.Sprintf("JSON marshal error: %v", err)}
	}
	return Do(http.MethodPost, url, bytes.NewBuffer(b), "application/json")
}

// PatchJSON marshals payload to JSON and sends a PATCH request.
func PatchJSON(url string, payload any) Result {
	b, err := json.Marshal(payload)
	if err != nil {
		return Result{StatusCode: 500, Body: fmt.Sprintf("JSON marshal error: %v", err)}
	}
	return Do(http.MethodPatch, url, bytes.NewBuffer(b), "application/json")
}

// PutJSON marshals payload to JSON and sends a PUT request.
func PutJSON(url string, payload any) Result {
	b, err := json.Marshal(payload)
	if err != nil {
		return Result{StatusCode: 500, Body: fmt.Sprintf("JSON marshal error: %v", err)}
	}
	return Do(http.MethodPut, url, bytes.NewBuffer(b), "application/json")
}
