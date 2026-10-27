package monitor

// DefaultRegistry returns a registry with the built-in check types.
func DefaultRegistry() *Registry {
	r := NewRegistry()
	Register(r, Spec[httpProps]{Type: "http", Description: "HTTP request; passes on an accepted status and optional body text", Build: buildHTTP})
	Register(r, Spec[tcpProps]{Type: "tcp", Description: "TCP connect succeeds", Build: buildTCP})
	Register(r, Spec[systemdProps]{Type: "systemd", Description: "systemd unit is active", Build: buildSystemd})
	Register(r, Spec[dockerProps]{Type: "docker", Description: "Docker container is running and healthy", Build: buildDocker})
	Register(r, Spec[commandProps]{Type: "command", Description: "Command exits with the expected code (no shell)", Build: buildCommand})
	Register(r, Spec[heartbeatProps]{Type: "heartbeat", Description: "A heartbeat arrived within interval plus grace", Build: buildHeartbeat})
	Register(r, Spec[promProps]{Type: "prometheus", Description: "Prometheus instant query satisfies a comparison", Build: buildPrometheus})
	return r
}
