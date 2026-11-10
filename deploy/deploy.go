// Package deploy embeds the host installation files that "lcm host apply"
// installs, so that the binary carries the exact files of its own commit. It
// holds data only; the files are also meant to be read and reviewed as they
// are.
package deploy

import "embed"

// Files holds the installed host files by their name in this directory.
//
//go:embed lcm.service lcm.env sudoers self-install machine-power machine-power-sudoers lcm-deploy lcm-deploy-sudoers lcm-ingress-reload security/helper.py security/install.py security/runtime.py security/sudoers
var Files embed.FS
