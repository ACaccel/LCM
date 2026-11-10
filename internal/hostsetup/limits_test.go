package hostsetup

import (
	"context"
	"strings"
	"testing"
)

func TestSandboxLimitsAreReportedInTheDeployStep(t *testing.T) {
	th := deployHost(t)
	out := th.converge()
	if strings.Contains(out, "WARNING") {
		t.Fatalf("a host with libbpf and cgroup v2 got a warning:\n%s", out)
	}

	th.r.systemdVersion = "systemd 255 (255.4-1ubuntu8.18)\n+PAM +AUDIT -BPF_FRAMEWORK -XKBCOMMON\n"
	_, out, err := th.apply(Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "deploy:          unchanged (WARNING: this systemd is built without BPF_FRAMEWORK") ||
		!strings.Contains(out, "per-service port restriction is not enforced") || strings.Contains(out, "cgroup v2") {
		t.Fatalf("output lacks the BPF_FRAMEWORK warning:\n%s", out)
	}
	if got := SandboxLimits(context.Background(), th.host); len(got) != 1 {
		t.Fatalf("limits = %q", got)
	}

	th.fs.remove("/sys/fs/cgroup/cgroup.controllers")
	if got := SandboxLimits(context.Background(), th.host); len(got) != 2 || !strings.Contains(got[1], "IPAddressDeny=") {
		t.Fatalf("limits = %q", got)
	}
}
