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
  - there's no systemd to keep it running across reboots.

##### Starting it automatically (so you don't have to by hand every time)

**Option 1 - shell profile (simplest, works today).** Add this to
`~/.bashrc`/`~/.zshrc`. It's idempotent - it only spawns a new service if one
isn't already answering on the socket, so it's safe to source in every new
terminal:

```bash
# Auto-start Podman's API socket (WSL2 has no systemd to do this for us)
if [ -n "$XDG_RUNTIME_DIR" ] && \
   ! curl -s --max-time 1 --unix-socket "$XDG_RUNTIME_DIR/podman/podman.sock" http://d/_ping >/dev/null 2>&1; then
  mkdir -p "$XDG_RUNTIME_DIR/podman"
  nohup podman system service --time=0 "unix://$XDG_RUNTIME_DIR/podman/podman.sock" \
    >/tmp/podman-service.log 2>&1 &
  disown
fi
export DOCKER_HOST="unix://$XDG_RUNTIME_DIR/podman/podman.sock"
```

**Option 2 - true WSL boot-time start (no shell required).** Recent WSL2
(≥ 0.67, `wsl --version` from Windows) can run a command as root when the
Linux VM itself boots, before any shell or login, via `/etc/wsl.conf`:

```ini
# /etc/wsl.conf
[boot]
command = "runuser -u <your-username> -- sh -c 'mkdir -p /run/user/$(id -u <your-username>)/podman && podman system service --time=0 unix:///run/user/$(id -u <your-username>)/podman/podman.sock &'"
```

Then from Windows (not inside WSL): `wsl --shutdown`, then reopen your distro
for it to take effect. Caveats: this always runs as root, so it needs
`runuser`/`su` to drop into your user for a *rootless* Podman socket; and on
WSLg setups `$XDG_RUNTIME_DIR` may actually be `/mnt/wslg/runtime-dir` rather
than `/run/user/<uid>` (check `echo $XDG_RUNTIME_DIR` in your normal shell
first and adjust the path above to match). Because of that path variance,
Option 1 is the more portable default - use Option 2 only if you specifically
want the socket up before any shell opens.

## RunC

Using this connector requires full privileges to the local runC root dir of container state (default: `/run/runc`)

#### Options

Var | Description
--- | ---
RUNC_ROOT | path to runc root for container state (default: `/run/runc`)
RUNC_SYSTEMD_CGROUP | if set, enable systemd cgroups
