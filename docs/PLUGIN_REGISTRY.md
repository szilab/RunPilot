# Plugin publication and registry

## Independent version domains

The first explicit backend and frontend contract baselines are both `1.0.0`.
They describe the existing implemented surfaces, not future capability plans.
Neither derives from the application version. For example, this checkout has:

| Domain | Example/current value | Meaning |
| --- | --- | --- |
| RunPilot | `1.0.3-dev` | Native application release |
| Raw WASM ABI | `1` or `2` | Calling convention selected by `requires.runpilotApi` |
| Backend contract | `1.0.0` | Implemented host capabilities, lifecycle, communication and ABI support |
| Frontend contract | `1.0.0` | `ws`, `navigation`, `overview`, `settings`, `ui` extension APIs |
| Plugin package | `0.1.0` (hello example) | Independently released plugin source and assets |

ABI v1 and the ABI-v2 host infrastructure remain supported. A backend contract
release can support multiple raw ABIs. `apiVersion: runpilot.plugin/v1` is the
manifest schema identifier, not another application compatibility check.

Plugins live in the main repository, normally under `plugins/<id>/`, with their
own `plugin.yaml`, optional `backend/`, optional `web/`, and optional changelog.
Existing dotted IDs such as `remote.xpra` remain valid even where source
folder names use hyphens. Releases use the manifest ID.

```yaml
apiVersion: runpilot.plugin/v1
id: hello
name: Hello extension
version: 0.1.0
requires:
  frontend: ">=1.0.0 <2.0.0"
frontend:
  module: web/plugin.js
```

A backend additionally declares `requires.backend` and `requires.runpilotApi`
(1 or 2). Omit the contract requirement for an absent component. No application
version requirement is needed. Existing local manifests must be updated to
strict `major.minor.patch` versions and declare ranges for their components;
invalid packages are reported as discovery errors, never rewritten in place.

Masterminds/semver v3 handles comparison and constraints, including numeric
ordering (`1.10.0 > 1.9.0`), conjunctions, exact versions, upper bounds, OR,
caret and tilde ranges. Contract prereleases do not satisfy ordinary stable
ranges unless the range explicitly admits prereleases (for example `>=1.0.0-0`).
Package prereleases are ordered using SemVer and may be selected when their
contract/platform requirements match. Build metadata does not imply an update.

## Release policy and build

The immutable GitHub plugin releases and their `publication.json` assets are
the publication source of truth. `plugin-catalog/catalog.json` is a generated,
replaceable discovery index rebuilt from every published release record; GitHub
Release titles and descriptions are not runtime metadata. The record carries the
plugin's display metadata, version, package URL/checksum and compatibility
requirements. There is no separate RunPilot version constraint: platform,
backend/frontend contract ranges and (for backends) raw WASM ABI determine
compatibility.

`plugins/publication.json` is repository-only publication policy. Each rule
maps an ID to its source directory and an explicit `publish` boolean. It is not
part of runtime manifests. System is `publish: false`; it remains a technical
ABI/frontend/WebSocket fixture and is no longer installed on fresh startup.
Existing manual System installations are not deleted. The Tasks plugin is
registered with `publish: false` until its legacy cutover is reviewed. The RDP
plugin is publishable after its backend, authenticated stream transport, target
management and session lifecycle implementation. The VNC plugin is publishable
after its plugin-owned target/session implementation and packaged noVNC client.
`remote.xpra` remains disabled while its migration is deferred. The hello example is
buildable and locally testable; opt in using a temporary policy for publication
tests.

Build a deterministic archive locally:

```sh
go run ./cmd/plugin-build -out dist plugins/examples/hello
```

The builder checks the ID, strict SemVer, contract constraints and declared
assets, rejects symlinks/reserved paths, sorts paths and fixes ZIP timestamps.
Output must be outside the source directory. Packages contain source-directory
files; do not put credentials or transient build files there. Archive compressed
and expanded sizes are bounded to 64 MiB. Artifacts belong in ignored `dist/`,
not normal Git history.

A publication uses tag/release `plugin-<id>-v<version>` and assets:

```text
<id>-<version>.rpplugin
<id>-<version>.rpplugin.sha256
publication.json
```

`publication.json` is an immutable one-version record derived from the built
archive, including SHA-256 and compatibility metadata. Generation checks the
exact tag against `plugin.yaml`. No source manifest is trusted in place of the
artifact at catalog-generation time.

The **Publish plugin version** workflow accepts an explicit ID and version on
`develop` or `main`; it never runs on pull requests or unrelated pushes. Its
read-only validation job checks policy/manifest/tag, compiles optional TinyGo
backend WASM, checks JS syntax, runs focused tests, builds the archive, checksum
and record. Only the publication job has `contents: write`. It rejects existing
releases and tags, uploads into a draft, then publishes once all assets exist.
It never replaces plugin assets. Version changes are required to publish again.

All plugin publications share one concurrency group to serialize catalog writes.
A failed draft upload is deliberately not silently resumed or replaced; inspect
and clean up an unpublished draft deliberately before retrying. A catalog-update
failure does not invalidate the immutable plugin release. Regenerate the catalog
from release records and upload it separately; do not rerun plugin publication
with the same version.

## Catalog schema and generation

`plugin-catalog/catalog.json` is the sole mutable GitHub release asset. The
default URL is:

```text
https://github.com/szilab/RunPilot/releases/download/plugin-catalog/catalog.json
```

Schema version 1 groups all published versions by ID:

```json
{
  "schemaVersion": 1,
  "plugins": [{
    "id": "hello",
    "name": "Hello extension",
    "latest": "0.1.0",
    "versions": [{
      "version": "0.1.0",
      "requires": {"frontend": ">=1.0.0 <2.0.0"},
      "url": "https://github.com/szilab/RunPilot/releases/download/plugin-hello-v0.1.0/hello-0.1.0.rpplugin",
      "sha256": "<64 hex characters>"
    }]
  }]
}
```

Each version may contain `platforms` (`linux`, `windows`; absent means either)
and the backend contract/raw ABI requirement. Names/descriptions live at plugin
level and come from the newest record. Latest means greatest SemVer, independent
of host compatibility. Clients separately select the greatest compatible version.

Repository tooling owns generation, sorting and validation:

```sh
# For a publishable package and an explicit policy:
go run ./cmd/plugin-catalog -package dist/hello-0.1.0.rpplugin \
  -policy /tmp/publication-policy.json -tag plugin-hello-v0.1.0 \
  -out dist/publication.json
# Deterministic local generation from one or more immutable records:
go run ./cmd/plugin-catalog -policy /tmp/publication-policy.json \
  -out dist/catalog.json dist/publication.json
# Recover/regenerate from every published plugin release (paginated):
go run ./cmd/plugin-catalog -github -repository szilab/RunPilot -out dist/catalog.json
```

Generation rejects duplicate versions and malformed records; clients also reject
duplicate plugin IDs, bad URLs/checksums, invalid constraints/platforms, unknown
schema fields and incorrect `latest` values. Records are sorted, not appended in
API response order. Missing release records are errors; regeneration cannot
silently drop a broken release. A nonpublishable package cannot produce a release
record. Generation applies current repository publication policy and omits records for
nonpublishable plugins, including System. Disabling publication deliberately
removes a plugin from discovery; its immutable release history remains intact.

## Configuration, client and Settings

One optional configuration field overrides the first-party default:

```yaml
pluginRegistry:
  url: http://127.0.0.1:8080/catalog.json
plugins:
  hello:
    enabled: false
```

The separate `pluginRegistry` field preserves the existing `plugins.<id>`
enablement map without reserving a plugin ID named `registry`. An empty URL uses
the default. HTTP is supported for local testing; production should use HTTPS.
The URL is administrator configuration, not a per-request browser URL.

Catalog/download requests have a 30-second context deadline, status checks and
1 MiB/64 MiB body limits. A missing/unreachable catalog is a visible, retryable
Settings error; installed plugins and core functionality still work. Settings
fetches on first visit and on **Check for updates**, not on each status poll.

Settings → Plugins merges installed packages with catalog entries and shows
installed/enabled/loaded versions, local/manual origin, latest published,
latest compatible, incompatibility reasons, updates, failures and restart state.
A new but incompatible major release does not conceal an older compatible one.
The backend re-fetches and validates catalog metadata on every explicit install.

Install/update downloads only after compatibility checks, verifies SHA-256,
validates the archive and rechecks platform/contracts and exact ID/version/
requirements/platform metadata against the catalog. It then reuses the atomic
installer. Updates install a new immutable version directory. Discovery uses
SemVer ordering; old installed versions remain available on disk. There is no
automatic install, downgrade or update.

Installation and enablement are separate. New packages are disabled by default.
Enable/disable persists desired state. Runtimes and frontend assets remain pinned
to startup activation until restart, including after rescan or update. A failed
backend is reported without preventing the application from starting.

Uninstall disables desired activation, removes packages from discovery and retains `<dataDir>/plugins/<id>/data`.
To keep active frontend assets usable until restart, package directories move to
a private retired directory, which the next startup removes. No mutable plugin
data is silently removed. Uninstall is restart-required.

Catalog installations carry host-owned `.runpilot-source.json` inside the atomic
installation with ID, version, registry and checksum. Archives cannot supply this
reserved file. Manual/local installations need no source metadata, remain usable
without any catalog entry, and do not receive catalog update suggestions.

For development, edit the source plugin under `plugins/<id>` and build a local
`.rpplugin` with `plugin-build`; this artifact is for local validation and is not
published. A package installed outside the catalog has no catalog update
tracking. The published workflow builds the same immutable package format, then
creates its tagged GitHub release and regenerates the catalog. There is not yet a
Settings workflow for installing a local archive.

Use `tools/test-plugin-local.sh plugins/tasks ./data` to install a local source
package under `data/plugins/<id>/releases/<version>`, then restart RunPilot.

Updates are detected only when the user checks the catalog, and installation is
explicit. A newly installed version is selected on the next startup; the running
version stays active until then. Older version directories are retained, but the
current manager always selects the highest installed SemVer and does not expose
a rollback selector. A backend load failure is reported and does not stop
RunPilot, but it does not automatically restore an older plugin version. A future
rollback control should let an administrator select a retained version and
restart; activation remains restart-based.

## Trust and future scope

SHA-256 verifies artifact integrity against the catalog. It does **not** defend
against compromise of the GitHub repository or catalog. The first-party repository
is the trust root. No signing, multi-registry federation, hot loading or automatic
updates are added.

Tasks and System feature development are not part of this work. Host metrics
presentation is planned through plugin/widget contributions; the existing System
fixture is not a permanent production feature. Widget contracts, executable
themes, feature migration and broader capability work remain separate tasks.
