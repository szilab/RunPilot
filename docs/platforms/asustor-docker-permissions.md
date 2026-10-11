# Docker access on ASUSTOR ADM

RunPilot's App Central package runs as the ADM `admin` account in the
`administrators` group. It does not run RunPilot as root, change Docker Engine
configuration, or change socket permissions. On the tested AS6702T with ADM
5.1.4, `/var/run/docker.sock` was owned by `root:root`, mode `0660`, and the
system had no `docker` group. The `admin` service account therefore could not
open the Docker Engine socket.

The Docker plugin uses the Docker CLI and Compose v2 under RunPilot's service
identity. With no Docker context or `DOCKER_HOST` override, the Linux CLI uses
`/var/run/docker.sock`. The plugin reports socket permission denial separately
from a missing socket, invalid endpoint, daemon outage, network failure, missing
CLI/Compose, and CLI/Engine API incompatibility. It does not treat a failed
connection as an empty, healthy Docker installation.

## Inspect the current setup

Run these read-only commands over SSH. They do not print Docker credentials or
container configuration:

```sh
id admin
pid=$(cat /volume1/.@plugins/AppCentral/runpilot/data/runpilot.pid)
sed -n '/^Uid:/p;/^Gid:/p;/^Groups:/p' "/proc/$pid/status"
stat -c '%A %a %U:%G %n' /var/run/docker.sock
grep '^docker:' /etc/group || echo 'No docker group is configured'
```

If RunPilot is not installed at that data path, use the PID file path shown by
the package's App Central configuration. Do not dump the full process
environment: it may contain secrets. A Docker context or `DOCKER_HOST` override
can select a different endpoint from the default socket; inspect only the
endpoint setting itself and keep credentials out of logs and support requests.

## Optional access for an administrator who accepts the risk

There is no package-managed Docker permission change. On the tested ADM setup,
there was no Docker group or verified ADM-supported persistent socket ACL/group
setting. The safest default is to leave Docker unavailable to RunPilot. Do not
run RunPilot as root, use `chmod 666`, add a general-purpose group without
review, or patch the Docker Engine package's startup scripts.

An administrator can make a temporary, account-specific ACL change to test
access, but should do so only after accepting that Docker API access is
effectively root-equivalent host authority. Docker can create privileged
containers and mount host paths. This optional ACL is lost when Docker Engine
recreates the socket and is not a supported persistent production
configuration:

```sh
sudo setfacl -m u:admin:rw /var/run/docker.sock
sudo -u admin docker info
```

Before changing the ACL, inspect and preserve its current value with
`sudo getfacl -p /var/run/docker.sock`. If a named `admin` ACL entry already
exists, do not use the simple revoke command below: preserve and restore the
original ACL instead. Keep any saved ACL file in an administrator-controlled
location.

Use the actual endpoint path if the Docker context selects another socket. If
`setfacl` is unavailable, do not install packages or substitute unrestricted
permissions; leave Docker disabled until an ADM-supported access-control
method is available. After a successful check, refresh the Docker page in
RunPilot. Socket ACL changes take effect without restarting RunPilot.

For persistent access, an administrator must first verify that the installed
Docker Engine version and ADM expose a supported way to create the socket with
access for a dedicated account or group. Verify the resulting owner, group,
mode/ACL, and `docker info` result after a Docker Engine restart. That Docker
service configuration is outside the RunPilot package and is not changed by
RunPilot. If no supported persistent setting exists, there is no automatic
least-privilege Docker integration on this ADM installation.

If the only change was to a socket ACL, revoke it with:

```sh
sudo setfacl -x u:admin /var/run/docker.sock
```

If restoring a saved ACL, use `sudo setfacl --restore=<saved-acl-file>`.

If an administrator changes the RunPilot account's groups through an
ADM-supported mechanism, restart only RunPilot so its process receives the new
group list. Use **App Central → RunPilot → Stop, then Start**, or the package
hook:

```sh
sudo /usr/local/AppCentral/runpilot/CONTROL/start-stop.sh restart
```

Do not restart Docker Engine or other NAS services for a RunPilot group change.

## Plugin status

The Docker page and Overview distinguish these conditions:

| State | Meaning |
| --- | --- |
| `permission-denied` | The current account cannot access the Docker endpoint. On the ASUSTOR App Central build, the Docker page links to this document. |
| `socket-missing` | The configured Unix socket path does not exist. |
| `endpoint-invalid` | The Docker CLI rejected the configured endpoint. |
| `daemon-unavailable` | The daemon refused or did not answer the connection. |
| `connection-failed` | A network route, reset, or TLS connection prevented access. |
| `api-incompatible` | The Docker CLI and Engine API versions do not match. |
| `cli-missing`, `compose-missing` | Docker CLI or Compose v2 is unavailable. |

The ASUSTOR guidance link appears only for `permission-denied` when the RunPilot
binary identifies itself as an App Central build. Other Linux hosts and other
Docker failures retain their own diagnosis without an ASUSTOR-specific link.
