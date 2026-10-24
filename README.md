# RunPilot

<div align="center">
  <img src="internal/web/static/runpilot-logo.png" width="120">
</div>

RunPilot is a lightweight Windows/Linux host-management application written in Go. One native service manages configured workloads and operations and exposes an embedded local web UI. On Linux the preferred deployment is the container image (see [Run In Docker](#run-in-docker-recommended-on-linux)); a native systemd user service remains available. Windows uses Windows Service Control Manager.

The normal UI provides plugin management and a host monitoring Overview with
CPU, memory, GPU when available, and filesystem usage. Tasks, Docker, Terminal,
Remote desktop access for RDP and VNC is provided by one first-party plugin;
installing and enabling it is explicit.
Legacy backup, storage and
software backends remain in the codebase until their plugins reach parity, but
their pages are hidden and their REST APIs are no longer mounted. Configured
Backup jobs keep running from the configuration file.

> **RunPilot provides the framework; plugins provide the features.**

Legacy backends remain in place until their plugin replacements reach parity.
First-party plugin source stays in this repository, while plugin
packages are versioned and published independently through the GitHub-backed
plugin catalog.

## Capabilities

The following capabilities include legacy backend functions whose pages are
currently hidden:

- Native service through Windows SCM or a Linux systemd user service
- Long-running process supervision with restart policies and backoff
- Interval, daily and cron scheduled jobs plus manual execution
- Typed backup jobs using Robocopy, Restic and rdiff-backup
- Captured stdout/stderr and execution history
- Local and Docker-backed storage access
- Software Management through the RunPilot-owned Scoop provider
- Interactive Terminal tabs backed by Linux PTY or Windows ConPTY
- Remote desktop access through the optional Remote Desktop plugin (RDP and VNC)
- Embedded authenticated web UI
- Plugin package/runtime support with independent backend/frontend contracts
- GitHub-backed plugin catalog with explicit install/update/enable lifecycle

## Architecture direction

The target core is a small plugin host: package/runtime management, versioned
host capabilities, one authenticated application WebSocket, the web shell,
shared UI/theme primitives and framework settings. Tasks, Docker, Terminal and
Remote Desktop are plugin-owned in the normal UI. Storage, Software and Backup remain
transitional core implementations. Xpra remains a temporary internal core exception
with no user-facing navigation while its plugin capabilities are designed.

Host CPU/GPU/memory/disk presentation is built into Overview, without expanding
the nonpublic System ABI fixture into a permanent feature.

See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for the architectural source of
truth, [docs/PLUGINS.md](docs/PLUGINS.md) for plugin development, and
[docs/PLUGIN_REGISTRY.md](docs/PLUGIN_REGISTRY.md) for publication and
installation.

## Run In Docker (recommended on Linux)

The repository's `Dockerfile` builds a Linux image with the tools the
first-party plugins use: Docker CLI and Compose v2, bash, Python 3 and the
OpenSSH client. The defaults need no extra settings:

```sh
docker build -t runpilot .
docker run -d --name runpilot --restart unless-stopped -p 9070:9070 \
  -v /var/lib/runpilot:/var/lib/runpilot \
  -v /var/run/docker.sock:/var/run/docker.sock \
  runpilot
```

Or use [packaging/docker/compose.yaml](packaging/docker/compose.yaml):
`docker compose -f packaging/docker/compose.yaml up -d --build`.

Open `http://<host>:9070` and complete administrator setup within five
minutes; restart the container to open a new setup window.

- **Data** lives in `/var/lib/runpilot`. Mount it at the **same path** on the
  host: Docker Compose projects managed by the Docker plugin pass relative bind
  mounts to the host's Docker Engine, which resolves them as host paths. The
  container logs a warning when the path differs or no volume is mounted.
- **User**: the entrypoint runs RunPilot as the unprivileged `runpilot` user
  (UID/GID 1000; change with `PUID`/`PGID`), hands it the data directory, and
  grants the mounted Docker socket's group. `PUID=0` runs as root; starting
  with `--user` skips these adjustments.
- **Init**: `tini` is the container's init process, so orphaned processes from
  tasks and terminals are reaped without `--init`.
- **Extra packages**: `RUNPILOT_EXTRA_PACKAGES="ffmpeg git"` installs Alpine
  packages (space- or comma-separated, version constraints allowed) with `apk`
  before RunPilot starts. A restarted container keeps them; a recreated one
  installs them again. Invalid names stop the container; a failed download
  only logs a warning. It needs the default root start (not `--user`).
- **Docker socket** is optional; without it the Docker plugin reports that no
  Engine is available. Access to the socket is equivalent to root on the host.

Inside a container, Tasks and Terminal run in the container, with its tools
and mounted directories, not on the host. Overview detects the container
(`/.dockerenv`, `/run/.containerenv` or `RUNPILOT_CONTAINER`) and shows the
container's own CPU and memory use against its limits, the host totals for
context, and the container and mounted filesystems; the host-wide load
average is hidden. Inside the container `localhost` is the container itself;
Web Apps and Remote targets on the host use the host's address or
`host.docker.internal`.

## Install On Linux

Install the latest `main` release with:

```bash
curl -fsSL https://raw.githubusercontent.com/szilab/RunPilot/main/install.sh | sh
```

Use a custom data directory or the latest `develop` prerelease with environment
variables:

```bash
curl -fsSL https://raw.githubusercontent.com/szilab/RunPilot/main/install.sh | env RUNPILOT_DATA_DIR=/opt/runpilot-data sh
curl -fsSL https://raw.githubusercontent.com/szilab/RunPilot/main/install.sh | env RUNPILOT_CHANNEL=develop sh
```

The installer downloads `runpilot-linux-amd64` from the rolling GitHub release,
verifies its `.sha256` file when `sha256sum` is available, installs it to
`$HOME/.local/bin/runpilot`, installs the Linux systemd user service, and starts
it. Before making changes, it prints the selected release, install path, data
directory, and service action, then asks for confirmation. Re-run the same
command to update an existing user-service installation; an active RunPilot user
service is stopped before the binary is replaced and then started again. Set
`RUNPILOT_INSTALL_DIR`, `RUNPILOT_START_SERVICE=0`, `RUNPILOT_SKIP_SERVICE=1`,
or `RUNPILOT_ASSUME_YES=1` to customize installer behavior. Non-interactive
runs, including scheduled RunPilot tasks, exit before downloading unless
`RUNPILOT_ASSUME_YES=1` is set. If a non-interactive run finds the RunPilot user
service already active, it refuses to stop that service unless
`RUNPILOT_ALLOW_SERVICE_RESTART=1` is also set; use `RUNPILOT_SKIP_SERVICE=1`
for a scheduled binary-only update that should not restart the service running
the task.

### Automatic Updates From A RunPilot Task

A RunPilot scheduled task can update the installed binary non-interactively and
ask systemd to restart RunPilot after the task has had time to finish recording
its result. Because a RunPilot `sh` interpreter runs a script path, use a
direct `/bin/sh -c` command for an inline pipeline:

The YAML example below is a legacy job definition. Create an equivalent
scheduled command in the Tasks plugin UI when using the plugin.

```yaml
name: RunPilot update
enabled: true
type: command
schedule:
  type: daily
  timeOfDay: "01:00"
overlapPolicy: skip
command:
  path: /bin/sh
  args:
    - -c
    - 'curl -fsSL https://raw.githubusercontent.com/szilab/RunPilot/main/install.sh | env RUNPILOT_ASSUME_YES=1 RUNPILOT_SKIP_SERVICE=1 sh && systemd-run --user --on-active=30s systemctl --user restart runpilot.service'
  interpreter: direct
```

Use the same task with `RUNPILOT_CHANNEL=develop` before the installer command
to track the develop rolling release:

```yaml
command:
  path: /bin/sh
  args:
    - -c
    - 'curl -fsSL https://raw.githubusercontent.com/szilab/RunPilot/main/install.sh | env RUNPILOT_CHANNEL=develop RUNPILOT_ASSUME_YES=1 RUNPILOT_SKIP_SERVICE=1 sh && systemd-run --user --on-active=30s systemctl --user restart runpilot.service'
  interpreter: direct
```

In the GUI, choose **Direct executable**, enter `/bin/sh` as the executable or
script path, and add `-c` followed by the complete pipeline as the arguments.
The arguments field parses quotes itself, so enter the pipeline as one quoted
argument, for example: `-c "curl ... | env ... sh && systemd-run ..."`.

`RUNPILOT_ASSUME_YES=1` allows the unattended installer to proceed and
`RUNPILOT_SKIP_SERVICE=1` prevents it from restarting the service while the
task is still recording its result. The deferred restart is important:
restarting `runpilot.service` directly from the task can stop RunPilot before
it records the final task status.

The installer compares the selected release version with the installed binary
and exits without replacing or restarting RunPilot when that version is already
installed.

Automatic self-updates can temporarily or permanently cut off web and terminal
access if the downloaded binary is broken, the service unit is misconfigured, or
the restart fails. Keep another way to reach the host, such as SSH, local console
access, or a separate systemd user session, before enabling an unattended update
task.

## Development

```bash
go test ./...
go vet ./...
go run ./cmd/runpilot run --data-dir "$HOME/runpilot-data"
```

On Linux, install and start the user service with `runpilot service install` and
`runpilot service start`. The service runs with the installing user's permissions
and does not require a graphical login. To keep it running and start it at boot
without an interactive login, enable lingering for that user:

```bash
sudo loginctl enable-linger <username>
```

Open `http://127.0.0.1:9070`. A fresh installation allows administrator setup
for five minutes after startup. When the window ends, setup is locked until you
restart RunPilot to open another five-minute window. Choose a username and a
password of at least 12 characters, confirm the password, and select the RunPilot
data directory. The username defaults to `admin`, and the data directory
defaults to the current RunPilot data root. RunPilot stores only a salted
Argon2id password hash in `runpilot.yaml`. Use `--data-dir PATH` to pin the
application data root, and `--config-dir PATH` to store `runpilot.yaml` in a
separate configuration directory. An explicit `--data-dir` makes the data
root fixed in setup. `--config-dir` only controls where `runpilot.yaml` is
stored, and setup can still select the data root.

Browser login uses server-managed HttpOnly sessions with CSRF protection,
expiration, logout and revocation. Use HTTPS for credentials and session cookies
when clients cross a network. Fresh installations disable bearer API-token auth;
Settings can enable it for CLI or integrations. Existing token installations
preserve that API path during migration. Upgrades never reset credentials.

If the administrator password is lost, stop RunPilot and run
`runpilot reset-admin` locally as the RunPilot service account, then restart
RunPilot and create a new administrator within the setup window. Pass
`--data-dir` when RunPilot was deployed with an explicit data directory.

The default application data directory is `%LOCALAPPDATA%\RunPilot` on Windows,
`$XDG_DATA_HOME/runpilot` or `~/.local/share/runpilot` on Linux, and
`/var/lib/runpilot` in the container image. The setup page accepts
an absolute path accessible to the RunPilot service identity. It refuses symlink
paths and unrelated destination contents, and does not change ownership or
permissions. Data-root precedence is explicit `--data-dir`,
`RUNPILOT_DATA_DIR`, the persisted deployment locator, then the platform default.
The locator is stored outside the selected data root. The container image sets
`RUNPILOT_DATA_DIR`, so the data directory is chosen by the volume mount rather
than in Settings.

After setup, change the data root from **Settings → RunPilot data directory**.
RunPilot shuts down its managed activity, copies and verifies state, then updates
the locator and restarts. A failed copy leaves the original root active. The
operation requires a new destination; it will not merge existing contents.
Docker Compose projects require a manual move because relative bind mounts depend
on their original paths. Data stored outside RunPilot's data root also needs its
own migration plan. Deployments installed with an explicit `--data-dir` override
must update that deployment setting directly.

In the container image, recover the administrator with the same data mount
while the RunPilot container is stopped:

```sh
docker stop runpilot
docker run --rm -v /var/lib/runpilot:/var/lib/runpilot runpilot reset-admin
docker start runpilot
```

Then complete setup within five minutes of the start.

## Web server and reverse proxy

The YAML `server` section controls the listener and UI URL prefix:

```yaml
server:
  bind: 127.0.0.1:9070 # Host part is used with port; full legacy address also works.
  port: 9070
  basePath: /runpilot
  websocketPayloadMode: disabled # disabled, optional, or required
```

### WebSocket payload encryption

RunPilot can encrypt application payloads inside the existing WSS connection with
ephemeral P-256 ECDH, HKDF-SHA-256, and AES-256-GCM. This protects payload
contents from passive TLS-inspecting proxies; connection endpoints, timing,
frame sizes, and traffic volume remain visible, and WSS remains required.

`disabled` preserves current WebSocket behavior. `optional` negotiates encryption
with a compatible client but permits an unencrypted legacy client, so it is
compatible but vulnerable to an active downgrade. `required` rejects clients
that do not complete the secure negotiation and never falls back to plaintext.
Each connection creates fresh ephemeral keys and directional sequence numbers;
closing or reconnecting discards the session.

The current browser deployment has no independently provisioned server identity
trust anchor. The handshake therefore protects against passive observation, but
does not claim protection against an active application-layer MITM that can
substitute both handshake keys. Do not call this E2EE.

`--port` and `--base-path` override these values for one foreground run:

```powershell
.\runpilot.exe run --port 9080 --base-path /runpilot
```

Use the same flags with `service install` to persist them in the installed
service command line. With the example above, publish the application
through a reverse proxy at `https://mydomain.com/runpilot/`, forwarding that
prefix unchanged to RunPilot. The UI, REST API, and the application WebSocket that carries plugin terminal
and Docker container sessions all use this one RunPilot listener and configured
prefix. No terminal-specific port or proxy target is required. The browser
builds its `ws:`/`wss:` URL from the GUI page's base URI, so the public host,
port, TLS scheme, and prefix are preserved when TLS terminates at a reverse
proxy.

Configure a WebSocket-aware proxy to forward `/runpilot/*` (including Upgrade
requests) to the same RunPilot upstream port, preserving both the `/runpilot`
prefix and original `Host` header. Do not strip that prefix in the proxy when
RunPilot is configured with `basePath: /runpilot`.

### Single sign-on through an authenticating proxy

RunPilot can trust the user name set by an authenticating reverse proxy or
forward-auth gateway (for example Authelia, Authentik, oauth2-proxy or
Pomerium). Configure it in **Settings → Proxy single sign-on** or with two
environment variables; both values are required:

| Setting | Environment variable | Example | Meaning |
|---|---|---|---|
| Trusted proxies | `RUNPILOT_TRUSTED_PROXIES` | `192.0.2.10, 198.51.100.0/24` | Comma- or space-separated IP addresses and CIDR ranges of the proxies |
| User name header | `RUNPILOT_PROXY_AUTH_HEADER` | `Remote-User` | Header that carries the signed-in user name |

When either environment variable is set, the environment owns both values and
the Settings fields are read-only. Settings changes apply immediately and are
stored in `runpilot.yaml`; RunPilot refuses to save values under which the
browser saving them would be denied (a trusted proxy reporting another user).

When a request reaches RunPilot directly from a trusted address and the header
equals the RunPilot administrator's user name (exact, case-sensitive match),
RunPilot signs the browser in with an ordinary session. Requests from other
addresses ignore the header and use password sign-in or the API token as
usual; `X-Forwarded-For` and similar headers are never used to decide whether
a proxy is trusted. If a trusted proxy names a different user, or sends the
header more than once, RunPilot refuses the API request, even when it carries a
session or API token. A partial or invalid environment configuration stops
RunPilot at startup; invalid values edited into `runpilot.yaml` by hand leave
proxy sign-in off.

Requirements for a safe setup:

- The proxy must overwrite or remove a client-supplied header with the same
  name on every route to RunPilot, including routes it does not authenticate.
- Trust only the proxy's own address. A shared Docker network range also
  trusts every other container on that network; prefer a dedicated network or
  a fixed proxy address.
- Signing out in RunPilot ends only its session; the next request through the
  proxy signs in again. Sign out at the identity provider instead.

Common header names are `Remote-User` (Authelia), `X-authentik-username`
(Authentik), `X-Auth-Request-User` or `X-Forwarded-User` (oauth2-proxy) and
`X-Pomerium-Claim-User` (Pomerium); configure the proxy to pass the user name
that matches the RunPilot administrator.

## Interactive Terminal

The Terminal plugin starts interactive sessions as the RunPilot service
identity. It uses a real Linux PTY or Windows ConPTY with xterm.js assets
packaged in the plugin; no Node.js runtime or public CDN is required after
build. Terminal sessions use the common authenticated application WebSocket
and are never recorded as task history.

Terminal access is equivalent to arbitrary command execution as the account
running RunPilot. On Linux this is the installing user's normal account; on
Windows it is the configured service identity. It is protected by the same administrator session as process and job management. Plugin requests and session
events use the authenticated application WebSocket.

## Remote Desktop plugin

The Remote Desktop plugin provides RDP and VNC from one page. Install and enable
it in **Settings → Plugins**, then restart RunPilot. RDP uses its packaged
Guacamole client and the generic network stream capability; VNC packages noVNC
1.7.0 and uses the authenticated RunPilot application WebSocket for binary
stream traffic. Existing VNC targets are imported from an installed, enabled
legacy VNC plugin when the VNC tab is opened. Passwords are requested only when
needed and remain in memory for the active browser session.

Each target shows whether it is reachable: **Online** (the RDP/VNC port
accepts connections), **Host up, service off** (the computer answers but the
port is closed) or **Offline**. The list is checked every 30 seconds while the
page is open. **Wake** sends a Wake-on-LAN packet to the target's MAC address,
set under **Advanced settings**. When it is empty, RunPilot uses the address it
learned from the local network's ARP table while the computer was online, or
looks it up when Wake is pressed. Detection and Wake-on-LAN only work for
computers on the same local network as RunPilot; in a container, run RunPilot
with host networking. The target computer must have Wake-on-LAN enabled.

The old standalone VNC package is no longer published. After its targets have
been imported and verified, uninstall it from **Settings → Plugins**. Xpra's
legacy core runtime is temporarily retained without user-facing navigation. Its migration to a
`remote.xpra` plugin is deferred while reusable process, listener, and browser
publication capabilities are designed; Xpra is not currently available from
the RunPilot GUI.

## Windows build

```bash
GOOS=windows GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o dist/runpilot.exe ./cmd/runpilot
```

On Windows, from an elevated terminal:

```powershell
.\runpilot.exe service install
.\runpilot.exe service start
```

The default data directory is `%LOCALAPPDATA%\RunPilot`. Its persistent locator
is stored under `%APPDATA%\RunPilot\data-root`, independently of the selected
root. Older installations using `%ProgramData%\RunPilot` retain their existing
data directory and locator.

## Configuration model

The Tasks plugin presents continuous and scheduled command tasks. The pre-plugin process manager, command jobs, Tasks REST endpoints, and Tasks UI have been removed. Old `processes` and command-job entries are dropped when an older configuration is next opened; Backup jobs remain typed and scheduled in core until their own plugin migration.

Storage locations are runtime capabilities, not user-managed configuration records. `local` always exposes filesystem roots available to the RunPilot service identity. `docker-volumes` is one Docker-backed location whose root lists current volumes as directories. Volume contents are accessed through short-lived Docker helper containers, never through a Docker host mountpoint; running volumes are read-only.

Backup configuration is stored in the normal RunPilot YAML configuration.
Plugin-owned command tasks use the Tasks plugin's owner-scoped storage.

Example backup definition:

```yaml
id: job-example
name: Nightly data backup
enabled: true
type: backup
schedule:
  type: daily
  timeOfDay: "03:00"
overlapPolicy: skip
backup:
  engine: robocopy
  robocopy:
    source: D:\Data
    destination: F:\Backup\Data
    mode: copy
    retries: 2
    retryWaitSeconds: 5
```

`mirror` mode maps to Robocopy `/MIR` and can delete destination-only files. The hidden legacy editor retains its warning if it is reintroduced during migration.

Restic uses a repository and native snapshots; it can run `forget` retention (optionally with `--prune`) and a post-backup repository check. Passwords are not stored in configuration: use an optional `passwordFile` path or Restic's normal externally supplied environment.

```yaml
backup:
  engine: restic
  restic:
    repository: F:\Restic
    sources: [D:\Documents, D:\Photos]
    excludes: ["*.tmp"]
    tags: [home]
    useVss: true
    retention: { keepDaily: 7, keepMonthly: 12, prune: true }
    checkAfterBackup: true
```

rdiff-backup keeps its own directly browsable current mirror and historical increments at the destination. RunPilot can ask it to remove older increments and verify a completed backup.

```yaml
backup:
  engine: rdiff-backup
  rdiffBackup:
    source: D:\Photos
    destination: F:\Backup\Photos
    retention: { olderThan: 3M }
    verifyAfterBackup: true
```

Repository/version browsing and restore workflows are intentionally not part of the Backup capability yet.

Software Management is configured with a typed, RunPilot-owned Scoop provider:

```yaml
software:
  providers:
    - id: scoop
      name: RunPilot Scoop
      type: scoop
      scoop:
        root: D:\RunPilotApps # optional; empty uses <data-dir>\software\scoop
```

RunPilot downloads and bootstraps this isolated Scoop instance on first use, including Scoop's managed portable Git prerequisite under the same root. It never uses, changes, or imports an existing user Scoop installation; it does not permanently add Scoop shims to PATH or set global Scoop environment variables. Changing `root` selects a new isolated installation and leaves the old root untouched. Packages are standard portable Scoop packages and remain separate from legacy RunPilot Process definitions. The legacy Software API and hidden page support bucket management; the required `main` bucket, Git and 7-Zip cannot be individually removed because Scoop manages them.

## Architecture

```text
runpilot.exe
├── Windows Service host
├── Core controller
│   ├── Scheduler and retained Backup runner
│   └── Typed external integrations
├── Future storage providers / software providers
├── Runtime PTY/ConPTY terminal sessions (not persisted)
├── YAML config + plugin-owned SQLite history + Backup logs
└── Embedded HTTP API + web UI
```

Plugin execution history and logs live in each plugin's `data` directory under
`<data-dir>/plugins/<id>/`. Retained Backup logs use `<data-dir>/internal/runs/`;
RunPilot does not create `<data-dir>/legacy/` or a global legacy history DB.

## Docker Compose (Linux)

RunPilot has a deliberately narrow, Linux-only Docker Compose v2 feature. Managed projects are directories below `<data-dir>/compose/<project-name>/`; each can contain `compose.yaml` and a secret-bearing `.env` file. Project directories are the registry, so a managed project appears before its first `up`.

The legacy Docker backend (hidden page, unmounted API) manages projects, volumes, and networks; the Docker plugin is the user-facing implementation. They discover other Compose projects through Docker too, but leave them read-only. RunPilot never adopts or edits them. Managed projects expose only `up -d`, `start`, `stop`, and `down`; commands always include the project name, project directory, and Compose file. `down` does not remove volumes or images. A managed directory can be deleted only after Docker confirms there are no containers with its Compose project label. Containers in managed projects can be started, stopped, and deleted only when stopped; their logs can be viewed and a running container can open a PTY-backed `docker exec -it <id> /bin/sh` terminal. These actions validate the container ID, Compose project label, and managed-project directory before using fixed Docker arguments; they are not a general container-control or command API.

Volumes are discovered with Docker metadata and show Compose ownership only when Compose labels exist. Every discoverable volume is automatically a Storage location. RunPilot can create named local-driver volumes and may delete an unreferenced volume; deletion never uses force. Non-local drivers remain visible but unavailable for browsing. Running-volume Storage is read-only.

Docker volume mountpoints are resolved only through `docker volume inspect` internally and are never sent through the Storage API. This prepares a future provider-neutral Backup source flow; no Docker volume backup job exists yet. A filesystem backup of a volume used by a running application, especially a database, is not automatically application-consistent. Future backup support must explicitly require downtime or provide a separate quiescing strategy.

The legacy backend also manages Docker networks. RunPilot can create named bridge networks and delete an unused network only after Docker confirms that no running or stopped container references it. Compose ownership is shown only from Compose labels. It does not expose network driver options, attach/detach controls, firewall configuration, or generic Docker networking commands.

Docker authority is exactly the authority of the process running RunPilot. The legacy backend tests the Docker CLI, Compose v2 plugin, and daemon access under that identity, distinguishing missing CLI/plugin, unavailable daemon, and permission denial. It never invokes sudo, changes Docker socket permissions, or modifies group membership.

## Near-term roadmap

1. Replace the initial `taskkill /T` process-tree termination with native Windows Job Objects.
2. Add graceful stop policies and configurable stop timeouts.
3. Add live log streaming (SSE/WebSocket), rotation and retention.
4. Add job cancellation and richer running-job state.
5. Add Restic snapshot/version browsing, download and restore. Its read/version capabilities will intentionally differ from the writable Local Filesystem provider.
7. Add encrypted secret/environment/integration credential storage using Windows DPAPI or an equivalent protected mechanism.
8. Add a double-click/tray management shell for install/start/stop/open-UI actions.
9. Add further typed Software providers such as WinGet for conventional Windows software where appropriate.
10. Add signed Windows releases and GitHub Actions CI/release builds.
