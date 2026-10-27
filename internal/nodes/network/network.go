// Package network provides the nodes that talk to HTTP services:
// http.request, http.get and ci.status.
package network

import (
	"errors"
	"net/http"

	"github.com/ACaccel/lcm/internal/node"
	"github.com/ACaccel/lcm/internal/nodes/internal/httpcall"
	"github.com/ACaccel/lcm/internal/secret"
)

// CategoryNetwork is the category of the nodes in this package.
const CategoryNetwork = "network"

// DefaultGitHubAPI is the API root used by ci.status.
const DefaultGitHubAPI = "https://api.github.com"

// Deps are the collaborators of the network nodes.
type Deps struct {
	// Client defaults to a client that follows redirects only within one host.
	Client *http.Client
	// Secrets resolves bearerSecret, headerSecrets and tokenSecret names.
	Secrets secret.Source
	// GitHubAPI overrides the API root, for GitHub Enterprise and tests.
	GitHubAPI string
}

func (d Deps) client() *http.Client {
	if d.Client != nil {
		return d.Client
	}
	return httpcall.NewClient()
}

func (d Deps) githubAPI() string {
	if d.GitHubAPI != "" {
		return d.GitHubAPI
	}
	return DefaultGitHubAPI
}

// Register adds the network node types to r.
func Register(r *node.Registry, d Deps) {
	registerRequest(r, d)
	registerCI(r, d)
}

// failure converts a request error into a node error: client errors will
// not change on retry, so they are permanent; server errors and transport
// failures are left retryable.
func failure(err error) error {
	var se *httpcall.StatusError
	switch {
	case errors.As(err, &se) && !se.Retryable():
		return node.Permanent(err)
	case errors.Is(err, secret.ErrNotFound), errors.Is(err, secret.ErrInvalidName):
		return node.Permanent(err)
	}
	return err
}
