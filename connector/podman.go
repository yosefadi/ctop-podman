package connector

import (
	"os"
	"path/filepath"

	api "github.com/fsouza/go-dockerclient"
)

func init() { enabled["podman"] = NewPodman }

// NewPodman connects to Podman's Docker-compatible API socket. Podman itself
// is not modified here - all wire-level differences from Docker are handled
// in newDockerConnector/watchEvents, since Podman's compat API is served
// through the same client used for the "docker" connector.
//
// Unlike Docker, Podman does not run a persistent background daemon by
// default - its socket must already be active, e.g.:
//   - systemd hosts: `systemctl --user enable --now podman.socket`
//   - non-systemd hosts: `podman system service --time=0 unix://$XDG_RUNTIME_DIR/podman/podman.sock &`
func NewPodman() (Connector, error) {
	if os.Getenv("DOCKER_HOST") == "" {
		host := os.Getenv("CONTAINER_HOST") // Podman's own conventional env var
		if host == "" {
			host = defaultPodmanHost()
		}
		if host != "" {
			os.Setenv("DOCKER_HOST", host)
		}
	}

	client, err := api.NewClientFromEnv()
	if err != nil {
		return nil, err
	}
	return newDockerConnector(client)
}

// defaultPodmanHost resolves the well-known rootless and rootful Podman
// socket locations, in that order, returning the first one that exists on
// disk. Returns "" if neither is present, leaving api.NewClientFromEnv() to
// fall back to its own default.
func defaultPodmanHost() string {
	if runtimeDir := os.Getenv("XDG_RUNTIME_DIR"); runtimeDir != "" {
		rootless := filepath.Join(runtimeDir, "podman", "podman.sock")
		if _, err := os.Stat(rootless); err == nil {
			return "unix://" + rootless
		}
	}
	const rootful = "/run/podman/podman.sock"
	if _, err := os.Stat(rootful); err == nil {
		return "unix://" + rootful
	}
	return ""
}
