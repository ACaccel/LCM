package monitor

import (
	"errors"
	"fmt"

	"github.com/ACaccel/lcm/internal/model"
)

// CheckTypes lists the registered check types, sorted by name.
func (m *Monitor) CheckTypes() []TypeInfo { return m.registry.Types() }

// ValidateChecks builds every check against the check registry without
// scheduling anything and returns the problems Sync would report for them.
// path is the location of the list in the document, such as "spec.checks";
// issue paths are relative to it, e.g. "spec.checks[1].props.url".
func (m *Monitor) ValidateChecks(checks []model.Check, path string) model.Issues {
	var out model.Issues
	for i, c := range checks {
		_, err := m.registry.Build(c.WithDefaults(), m.env)
		if err == nil {
			continue
		}
		prefix := fmt.Sprintf("%s[%d]", path, i)
		var is model.Issues
		if errors.As(err, &is) {
			for _, x := range is {
				p := prefix
				if x.Path != "" {
					p += "." + x.Path
				}
				out = append(out, model.Issue{Path: p, Message: x.Message})
			}
			continue
		}
		out = append(out, model.Issue{Path: prefix, Message: err.Error()})
	}
	return out
}
