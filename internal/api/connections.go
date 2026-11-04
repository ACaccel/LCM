package api

import (
	"net/http"
	"slices"

	"github.com/ACaccel/lcm/internal/resource"
)

// connectionDTO describes a host-configured connection. Addresses,
// usernames and secrets are host configuration and never leave the server.
type connectionDTO struct {
	Name           string   `json:"name"`
	Type           string   `json:"type"`
	AllowActions   []string `json:"allowActions"`
	AllowResources []string `json:"allowResources"`
	// Resources names the managed and retired resources whose document
	// names the connection.
	Resources []string `json:"resources"`
}

func (s *Server) handleListConnections(w http.ResponseWriter, r *http.Request) {
	p, ok := pageParams(w, r)
	if !ok {
		return
	}
	used := map[string][]string{}
	if s.resources != nil {
		recs, err := s.resources.List(r.Context(), resource.Filter{IncludeRetired: true})
		if err != nil {
			s.fail(w, r, err, nil)
			return
		}
		for _, rec := range recs {
			if name := rec.Document.Spec.Connection; name != "" {
				used[name] = append(used[name], rec.Name)
			}
		}
	}
	list := s.connections.List()
	items := make([]connectionDTO, 0, len(list))
	for _, c := range list {
		d := connectionDTO{Name: c.Name, Type: c.Type, AllowActions: c.AllowActions, AllowResources: c.AllowResources, Resources: used[c.Name]}
		if d.AllowActions == nil {
			d.AllowActions = []string{}
		}
		if d.AllowResources == nil {
			d.AllowResources = []string{}
		}
		if d.Resources == nil {
			d.Resources = []string{}
		}
		slices.Sort(d.Resources)
		items = append(items, d)
	}
	page, next, ok := pageSlice(w, p, items)
	if !ok {
		return
	}
	writeList(w, page, next)
}
