package api

import (
	"bytes"
	"encoding/base64"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ACaccel/lcm/internal/model"
)

// Page size limits shared by every list endpoint.
const (
	defaultPageSize = 50
	maxPageSize     = 500
)

// pageReq is a parsed limit and cursor pair.
type pageReq struct {
	limit  int
	cursor string
}

// pageParams reads the limit and cursor query parameters. Cursors are
// opaque to clients; they wrap whatever position marker the endpoint uses.
func pageParams(w http.ResponseWriter, r *http.Request) (pageReq, bool) {
	limit, ok := intParam(w, r, "limit", defaultPageSize, 1, maxPageSize)
	if !ok {
		return pageReq{}, false
	}
	p := pageReq{limit: int(limit)}
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		b, err := base64.RawURLEncoding.DecodeString(raw)
		if err != nil || len(b) == 0 || len(b) > 256 {
			writeError(w, http.StatusBadRequest, codeBadRequest, "invalid cursor", map[string]any{"param": "cursor"})
			return pageReq{}, false
		}
		p.cursor = string(b)
	}
	return p, true
}

func encodeCursor(marker string) *string {
	if marker == "" {
		return nil
	}
	s := base64.RawURLEncoding.EncodeToString([]byte(marker))
	return &s
}

// pageSlice returns the page of an in-memory list that the cursor selects;
// the cursor is the offset of the first item. On a bad cursor it has
// written a 400 response.
func pageSlice[T any](w http.ResponseWriter, p pageReq, items []T) (page []T, next *string, ok bool) {
	offset := 0
	if p.cursor != "" {
		n, err := strconv.Atoi(p.cursor)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, codeBadRequest, "invalid cursor", map[string]any{"param": "cursor"})
			return nil, nil, false
		}
		offset = n
	}
	if offset > len(items) {
		offset = len(items)
	}
	end := min(offset+p.limit, len(items))
	if end < len(items) {
		next = encodeCursor(strconv.Itoa(end))
	}
	return items[offset:end], next, true
}

// idCursor parses a cursor that wraps a positive row id.
func idCursor(w http.ResponseWriter, p pageReq) (int64, bool) {
	if p.cursor == "" {
		return 0, true
	}
	id, err := strconv.ParseInt(p.cursor, 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, codeBadRequest, "invalid cursor", map[string]any{"param": "cursor"})
		return 0, false
	}
	return id, true
}

// listBody is the response of every paged list endpoint.
type listBody[T any] struct {
	Items      []T     `json:"items"`
	NextCursor *string `json:"nextCursor"`
}

func writeList[T any](w http.ResponseWriter, items []T, next *string) {
	if items == nil {
		items = []T{}
	}
	writeJSON(w, http.StatusOK, listBody[T]{Items: items, NextCursor: next})
}

// timeParam reads an optional RFC 3339 query parameter.
func timeParam(w http.ResponseWriter, r *http.Request, name string) (time.Time, bool) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return time.Time{}, true
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, "invalid query parameter",
			map[string]any{"param": name, "expected": "RFC 3339 timestamp"})
		return time.Time{}, false
	}
	return t, true
}

// boolParam reads an optional boolean query parameter.
func boolParam(w http.ResponseWriter, r *http.Request, name string) (bool, bool) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return false, true
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, "invalid query parameter",
			map[string]any{"param": name, "expected": "true or false"})
		return false, false
	}
	return v, true
}

// pathVersion parses a version number from the path. A malformed one names
// no stored version, so it is a 404 like any other unknown version.
func pathVersion(w http.ResponseWriter, r *http.Request, name string) (int, bool) {
	n, err := strconv.Atoi(r.PathValue(name))
	if err != nil || n <= 0 {
		writeError(w, http.StatusNotFound, codeNotFound, "version not found", nil)
		return 0, false
	}
	return n, true
}

// readDocument reads a YAML or JSON document body (JSON is valid YAML, so
// both go to the same decoder). It has written the response on failure.
func readDocument(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	if ct := r.Header.Get("Content-Type"); ct != "" {
		mt, _, err := mime.ParseMediaType(ct)
		switch {
		case err != nil:
			writeError(w, http.StatusBadRequest, codeBadRequest, "invalid Content-Type", nil)
			return nil, false
		case mt != "application/yaml" && mt != "application/x-yaml" && mt != "text/yaml" && mt != "application/json":
			writeError(w, http.StatusBadRequest, codeBadRequest, "unsupported Content-Type",
				map[string]any{"contentType": mt, "supported": []string{"application/yaml", "application/json"}})
			return nil, false
		}
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, model.MaxDocumentSize))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, codeBadRequest, "document too large",
				map[string]any{"limitBytes": model.MaxDocumentSize})
			return nil, false
		}
		writeError(w, http.StatusBadRequest, codeBadRequest, "cannot read request body", nil)
		return nil, false
	}
	if len(bytes.TrimSpace(body)) == 0 {
		writeError(w, http.StatusBadRequest, codeBadRequest, "request body is empty", nil)
		return nil, false
	}
	return body, true
}

// decodeOptionalJSON is decodeJSON for bodies that may be absent.
func decodeOptionalJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, codeBadRequest, "request body too large", nil)
			return false
		}
		writeError(w, http.StatusBadRequest, codeBadRequest, "cannot read request body", nil)
		return false
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return true
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	return decodeJSON(w, r, dst)
}

// ifMatch is the parsed If-Match header of a document write.
type ifMatch struct {
	present bool
	// wildcard is true for "*", which only creates.
	wildcard bool
	version  int
}

// parseIfMatch reads one version, optionally quoted or weak ("3", "\"3\"",
// "W/\"3\""), or the wildcard.
func parseIfMatch(w http.ResponseWriter, r *http.Request) (ifMatch, bool) {
	raw := strings.TrimSpace(r.Header.Get("If-Match"))
	if raw == "" {
		return ifMatch{}, true
	}
	if raw == "*" {
		return ifMatch{present: true, wildcard: true}, true
	}
	raw = strings.TrimPrefix(raw, "W/")
	raw = strings.Trim(raw, `"`)
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		writeError(w, http.StatusBadRequest, codeBadRequest, "If-Match must be a document version or *", nil)
		return ifMatch{}, false
	}
	return ifMatch{present: true, version: n}, true
}

func int64String(n int64) string { return strconv.FormatInt(n, 10) }

// etag formats a version as a strong entity tag.
func etag(version int) string { return `"` + strconv.Itoa(version) + `"` }

// asIssues reports whether err is a model.Issues and stores it in dst.
func asIssues(err error, dst *model.Issues) bool { return errors.As(err, dst) }

// issueBody is one entry of details.issues.
type issueBody struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

func issuesBody(is model.Issues) []issueBody {
	out := make([]issueBody, 0, len(is))
	for _, i := range is {
		out = append(out, issueBody{Path: i.Path, Message: i.Message})
	}
	return out
}

func writeIssues(w http.ResponseWriter, is model.Issues) {
	writeError(w, http.StatusUnprocessableEntity, codeValidation, "validation failed",
		map[string]any{"issues": issuesBody(is)})
}

// writeFieldError reports a single invalid field as a validation failure.
func writeFieldError(w http.ResponseWriter, path, message string) {
	writeIssues(w, model.Issues{{Path: path, Message: message}})
}
