package monitor

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/ACaccel/lcm/internal/model"
)

var (
	// Unit names and container names must not start with "-" so they can
	// never be read as options by systemctl or docker.
	unitRe      = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9:_.@\\-]*$`)
	containerRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
)

// firstLine returns the first non-empty line of s, capped for storage.
func firstLine(s string) string {
	for line := range strings.SplitSeq(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return truncate(line, maxMessage)
		}
	}
	return ""
}

// runFailure converts a Runner error into a failed Result.
func runFailure(ctx context.Context, err error) Result { return failure(ctx, err) }

type systemdProps struct {
	Unit  string `json:"unit"`
	Scope string `json:"scope"`
}

func buildSystemd(_ model.Check, p systemdProps, env Env) (Built, error) {
	if !unitRe.MatchString(p.Unit) {
		return Built{}, propError("unit", "invalid unit name %q", p.Unit)
	}
	args := []string{"is-active", p.Unit}
	switch p.Scope {
	case "", "system":
	case "user":
		args = append([]string{"--user"}, args...)
	default:
		return Built{}, propError("scope", "must be system or user")
	}
	return Built{Probe: func(ctx context.Context) Result {
		start := time.Now()
		res, err := env.Runner.Run(ctx, Command{Name: "systemctl", Args: args})
		if err != nil {
			return runFailure(ctx, err)
		}
		state := strings.TrimSpace(res.Stdout)
		if res.ExitCode == 0 && state == "active" {
			return Result{OK: true, Latency: time.Since(start), Message: "active"}
		}
		if state == "" {
			state = fmt.Sprintf("systemctl exited with %d", res.ExitCode)
		}
		return Result{Latency: time.Since(start), Message: "unit is " + firstLine(state)}
	}}, nil
}

type dockerProps struct {
	Container string `json:"container"`
}

// dockerFormat prints "<status> <health>"; health is empty when the image
// defines no healthcheck.
const dockerFormat = "{{.State.Status}} {{if .State.Health}}{{.State.Health.Status}}{{end}}"

func buildDocker(_ model.Check, p dockerProps, env Env) (Built, error) {
	if !containerRe.MatchString(p.Container) {
		return Built{}, propError("container", "invalid container name %q", p.Container)
	}
	args := []string{"inspect", "--format", dockerFormat, p.Container}
	return Built{Probe: func(ctx context.Context) Result {
		start := time.Now()
		res, err := env.Runner.Run(ctx, Command{Name: "docker", Args: args})
		if err != nil {
			return runFailure(ctx, err)
		}
		if res.ExitCode != 0 {
			msg := firstLine(res.Stderr)
			if msg == "" {
				msg = fmt.Sprintf("docker inspect exited with %d", res.ExitCode)
			}
			return Result{Latency: time.Since(start), Message: msg}
		}
		fields := strings.Fields(res.Stdout)
		if len(fields) == 0 {
			return Result{Latency: time.Since(start), Message: "docker inspect returned no state"}
		}
		latency := time.Since(start)
		switch {
		case fields[0] != "running":
			return Result{Latency: latency, Message: "container is " + fields[0]}
		case len(fields) > 1 && fields[1] != "healthy":
			return Result{Latency: latency, Message: "container health is " + fields[1]}
		}
		return Result{OK: true, Latency: latency, Message: "running"}
	}}, nil
}

type commandProps struct {
	Command    []string `json:"command"`
	ExpectExit int      `json:"expectExit"`
}

func buildCommand(_ model.Check, p commandProps, env Env) (Built, error) {
	if len(p.Command) == 0 || p.Command[0] == "" {
		return Built{}, propError("command", "must be a non-empty argv list")
	}
	if p.ExpectExit < 0 || p.ExpectExit > 255 {
		return Built{}, propError("expectExit", "must be between 0 and 255")
	}
	cmd := Command{Name: p.Command[0], Args: p.Command[1:]}
	return Built{Probe: func(ctx context.Context) Result {
		start := time.Now()
		res, err := env.Runner.Run(ctx, cmd)
		if err != nil {
			return runFailure(ctx, err)
		}
		latency := time.Since(start)
		if res.ExitCode != p.ExpectExit {
			msg := fmt.Sprintf("exit code %d, want %d", res.ExitCode, p.ExpectExit)
			if out := firstLine(res.Stderr); out != "" {
				msg += ": " + out
			} else if out := firstLine(res.Stdout); out != "" {
				msg += ": " + out
			}
			return Result{Latency: latency, Message: truncate(msg, maxMessage)}
		}
		return Result{OK: true, Latency: latency, Message: fmt.Sprintf("exit code %d", res.ExitCode)}
	}}, nil
}
