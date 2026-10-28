// Package examples embeds the example documents shipped with LCM so that the
// binary can offer them. It holds data only; the directory is also meant to
// be read as documentation.
package examples

import "embed"

// Templates holds the policy templates, templates/<id>.yaml.
//
//go:embed templates/*.yaml
var Templates embed.FS
