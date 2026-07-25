# Connectors

`ctop` comes with the below native connectors, enabled via the `--connector` option.

Default connector behavior can be changed by setting the relevant environment variables.

## Docker

Default connector, configurable via standard [Docker commandline varaibles](https://docs.docker.com/engine/reference/commandline/cli/#environment-variables)

#### Options

Var | Description
--- | ---
DOCKER_HOST | Daemon socket to connect to (default: `unix://var/run/docker.sock`)

## Podman

Connects to Podman's Docker-compatible API socket, configurable via the same
`DOCKER_HOST` variable as the Docker connector (also honors Podman's own
`CONTAINER_HOST`). If neither is set, `ctop` auto-discovers the well-known
rootless (`$XDG_RUNTIME_DIR/podman/podman.sock`) then rootful
(`/run/podman/podman.sock`) socket paths.

#### Options

Var | Description
--- | ---
DOCKER_HOST | Daemon socket to connect to (checked first)
CONTAINER_HOST | Podman's own socket variable (checked if `DOCKER_HOST` is unset)

Unlike Docker, Podman does not run a persistent background daemon by default
- its API socket must already be active before starting `ctop`:

```
# systemd hosts
systemctl --user enable --now podman.socket

# non-systemd hosts (e.g. WSL2)
podman system service --time=0 unix://$XDG_RUNTIME_DIR/podman/podman.sock &
```

#### WSL2 (no systemd)

WSL2 typically has no systemd, so `podman.socket` is never auto-activated and
the socket has to be started by hand every session:

```bash
mkdir -p "$XDG_RUNTIME_DIR/podman"
podman system service --time=0 unix://$XDG_RUNTIME_DIR/podman/podman.sock &
disown
```

- `mkdir -p` is required - the parent directory usually doesn't exist yet, and
  `podman system service` fails with `bind: no such file or directory`
  without it.
- `$DOCKER_HOST` is often already exported to this same path by the
  `podman-docker` package; if so `ctop` (and the `docker` CLI, which some
  distros alias to Podman) will pick it up automatically once the service
  above is running.
- This only lasts for the current shell session/until the process is killed
  - there's no systemd to keep it running across reboots, so re-run it (or
  add it to your shell profile) whenever the socket disappears.

## RunC

Using this connector requires full privileges to the local runC root dir of container state (default: `/run/runc`)

#### Options

Var | Description
--- | ---
RUNC_ROOT | path to runc root for container state (default: `/run/runc`)
RUNC_SYSTEMD_CGROUP | if set, enable systemd cgroups
