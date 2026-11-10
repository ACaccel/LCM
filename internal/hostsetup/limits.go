package hostsetup

import (
	"context"
	"errors"
	"io/fs"
	"slices"
	"strings"
	"time"
)

// cgroupV2Marker exists where the unified cgroup hierarchy is mounted.
const cgroupV2Marker = "/sys/fs/cgroup/cgroup.controllers"

// SandboxLimits lists the sandbox restrictions of managed services that this
// host cannot enforce. systemd accepts SocketBindAllow= and IPAddressDeny=
// on every host but applies them only with cgroup BPF: socket binding needs
// a systemd built with libbpf (+BPF_FRAMEWORK in "systemctl --version",
// which Ubuntu 24.04 lacks) and the IP address filter needs cgroup v2. It
// only reads, and returns nothing when the host cannot be inspected at all.
func SandboxLimits(ctx context.Context, h Host) []string {
	var out []string
	if h.Runner != nil {
		version, err := h.Runner.Run(ctx, Command{Path: "systemctl", Args: []string{"--version"}, Timeout: 30 * time.Second, MaxOutput: 4096})
		switch {
		case err != nil:
			out = append(out, "cannot tell whether systemd enforces SocketBindAllow= (systemctl --version failed: "+Printable(err.Error(), 200)+"); a service may bind any port")
		case !slices.Contains(strings.Fields(string(version)), "+BPF_FRAMEWORK"):
			out = append(out, "WARNING: this systemd is built without BPF_FRAMEWORK (systemctl --version shows -BPF_FRAMEWORK), so SocketBindAllow=/SocketBindDeny= have no effect: "+
				"per-service port restriction is not enforced and a service can bind any port the kernel allows its account")
		}
	}
	if h.FS != nil {
		if _, err := h.FS.Lstat(cgroupV2Marker); errors.Is(err, fs.ErrNotExist) {
			out = append(out, "WARNING: cgroup v2 is not mounted at /sys/fs/cgroup, so IPAddressDeny= has no effect: "+
				"a build can reach the LCM API and other services on loopback")
		}
	}
	return out
}
