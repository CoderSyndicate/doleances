# Running doléances

This sets up a working copy of the register on one machine. You can use it to
try the software, or to rehost a register from a snapshot somebody else
published. The register is designed to be copied: a snapshot taken on one
installation restores onto another, and the copy serves the same doléances, the
same subjects and the same historical passages.

There are two ways to run it:

- **[With Docker](#1-the-images)**, using the published images and the compose
  file in this folder. This is the closest to a real deployment.
- **[With the binaries only](#binaries-only)**: three static executables, no
  Docker, no database server. This is the quickest way to look at a snapshot on
  a laptop.

Either way, a snapshot is then loaded from the console's **Snapshots** page
(see [Load a snapshot](#3-load-a-snapshot)).

## 1. The images

CI publishes each service as its own image on the GitHub Container Registry:

| image | role |
|---|---|
| `ghcr.io/codersyndicate/doleances/backend` | API, database, every write |
| `ghcr.io/codersyndicate/doleances/frontend` | the public site |
| `ghcr.io/codersyndicate/doleances/console` | curation and administration |

Each image is tagged several ways:

- `1.4.2` and `1.4`: a release and its minor line.
- `latest`: the newest release. It only exists once a version has been tagged.
- `sha-<full commit>` and `<short commit>`: one exact build. The short form is
  the same seven characters the binary reports as its commit, so a running
  service tells you which image it came from.

Fetch them:

```sh
docker pull ghcr.io/codersyndicate/doleances/backend:latest
docker pull ghcr.io/codersyndicate/doleances/frontend:latest
docker pull ghcr.io/codersyndicate/doleances/console:latest
```

> **While the repository is private, so are its images.** Log in first with a
> GitHub personal access token that has the `read:packages` scope:
>
> ```sh
> echo "$GITHUB_TOKEN" | docker login ghcr.io -u <your-github-user> --password-stdin
> ```

The images are deliberately bare: no shell, no package manager, a non-root user
(uid 65532), and a read-only filesystem at run time. You cannot `docker exec`
into them, and that is intended.

## 2. Start the services

[`docker-compose.yml`](docker-compose.yml) in this folder runs PostgreSQL,
the backend and the frontend. The console is optional and runs only with the
`console` profile:

```sh
docker compose -f docs/docker-compose.yml up -d                      # without console
docker compose -f docs/docker-compose.yml --profile console up -d    # with console
```

> **The console in this file runs with `--development`**
> (`DOLEANCES_DEVELOPMENT=true`), which **turns its authentication off**.
> Anyone who reaches port 8200 is a curator and an admin, and the console logs
> a warning saying so on every startup. That is why the port is published on
> `127.0.0.1` only. A real deployment signs curators in through Authentik
> instead; see [authentik-console-auth.md](authentik-console-auth.md).

To pin a release instead of `latest`:

```sh
DOLEANCES_TAG=1.4.2 docker compose -f docs/docker-compose.yml up -d
```

The register is then at **<http://localhost:8201>**.

| port | service | reachable from |
|---|---|---|
| `8201` | frontend | anywhere the machine is reachable |
| `8200` | console (`console` profile only) | **this machine only** (`127.0.0.1`) |
| `7300` | backend API, with its docs at `/docs` | **this machine only** (`127.0.0.1`) |

The backend is never meant to be public. It is published on loopback only so a
snapshot can also be restored with `curl` (see step 3).

### Settings worth knowing

Each setting is an environment variable read by the compose file. Set it in
your shell or in a `.env` file next to the compose file.

| variable | default | what it does |
|---|---|---|
| `DOLEANCES_TAG` | `latest` | the image tag to run |
| `DOLEANCES_SITE_URL` | `http://localhost:8201` | the public address. **Passkeys are bound to its host permanently**, so set the real one before anybody makes an account |
| `DOLEANCES_DB_PASSWORD` | `doleances` | the PostgreSQL password. Change it for anything that is not a local trial |
| `DOLEANCES_LLM_BASE_URL`, `DOLEANCES_LLM_API_KEY` | empty | an OpenAI-compatible endpoint. Without one, new submissions wait as *pending*, while everything already published or restored is served normally |
| `DOLEANCES_SETTINGS_KEY` | empty | encrypts the credentials the backend stores. Generate one with `openssl rand -base64 32` |

Every service flag can be set the same way: `--some-flag` becomes
`DOLEANCES_SOME_FLAG`.

### Is it up?

Each service also serves health and metrics on a separate maintenance port
(`9300` for the backend, `9201` for the frontend, `9200` for the console), under `/healthz/live`,
`/healthz/ready` and `/metrics`. The compose file does not publish those ports.
To check the services from outside, watch their logs:

```sh
docker compose -f docs/docker-compose.yml logs -f backend frontend
```

## Binaries only

Each release on GitHub carries one archive per platform, holding all three
binaries: `doleances-v<version>-<os>-<arch>.tar.gz` for `linux` or `darwin`,
on `amd64` or `arm64`, plus a `SHA256SUMS` file. The binaries are static, so
they need no runtime, no libc and no installation.

```sh
VERSION=v1.4.2
PLATFORM=darwin-arm64          # linux-amd64, linux-arm64, darwin-amd64

gh release download "$VERSION" -R CoderSyndicate/doleances \
  -p "doleances-$VERSION-$PLATFORM.tar.gz" -p SHA256SUMS
shasum -a 256 --ignore-missing -c SHA256SUMS

mkdir doleances && tar -xzf "doleances-$VERSION-$PLATFORM.tar.gz" -C doleances
cd doleances
```

On macOS, a binary downloaded through a browser is quarantined and refuses to
start. Clear the quarantine with `xattr -d com.apple.quarantine backend console
frontend`. A download through `gh` or `curl` is not quarantined.

Run each service in its own terminal, from that directory:

```sh
./backend                      # API on :7300, SQLite in ./doleances.db, snapshots in ./storage
./frontend                     # the register on http://localhost:8201
./console --development        # the console on http://localhost:8200, NO sign-in
```

With no other settings, the binaries use these defaults:

- **The backend uses SQLite**, in a `doleances.db` file in the current
  directory, so there is no database server to install. Snapshots it generates
  are written to `./storage`.
- **The frontend and the console find the backend** at
  `http://127.0.0.1:7300`, and the public address defaults to
  `http://localhost:8201`.
- **`--development` turns off the console's sign-in**, so you can reach the
  Snapshots page without an Authentik. It logs a warning on every startup. Use
  it only on a machine nobody else can reach. The backend and the frontend do
  not read this flag.

Every flag can also be set as an environment variable (`--database-dsn` becomes
`DOLEANCES_DATABASE_DSN`) or in a `config.yaml` file in the working directory.
`./backend --help` lists them all. For example, to point the backend at
PostgreSQL instead of SQLite:

```sh
./backend --database-driver postgres \
  --database-dsn 'postgres://doleances:secret@localhost:5432/doleances?sslmode=disable'
```

The binaries also serve health and metrics on their maintenance ports, `9300`,
`9201` and `9200`, under `/healthz/live`, `/healthz/ready` and `/metrics`.

## 3. Load a snapshot

A **snapshot** is a zip of readable files: one Markdown file per doléance, the
subjects, the historical passages, and a `MANIFEST.json` saying what it holds.
Any installation hands out its public one from the **Archive** page linked in
its footer, and you can download it directly:

```sh
curl -fLo register.zip https://<some-register>/archive/download
```

### From the console

Open the console's **Snapshots** page at <http://localhost:8200/snapshots>
(*Instantanés* in French). In the **Restore** section, choose the zip under
**Package file**, press **Restore**, and confirm. The console sends the file to
the backend and reports what was loaded, for example *"Restored a contributions
package: …"*.

### Without the console

The console only forwards the file to the backend's restore endpoint, so you
can also post it there directly. With Docker this works because the backend is
published on loopback; with the binaries, the backend is already on
`localhost:7300`.

```sh
curl -f -X POST \
  -H 'Content-Type: application/zip' \
  --data-binary @register.zip \
  'http://localhost:7300/v1/snapshots/restore?actor=initial-import'
```

The answer reports what was loaded:

```json
{
  "kind": "contributions",
  "counts":   { "messages": 1243, "subjects": 87, "historical": 40 },
  "restored": { "messages": 1243, "subjects": 87, "historical": 40 }
}
```

`actor` is the name recorded against the restore in the audit log. A restore
from the console records the console identity instead, which under
`--development` is `development (unauthenticated)`.

### Then

Reload <http://localhost:8201>: the register now shows the restored doléances.

**A restore replaces what the package covers.** It does not merge. A
*contributions* package replaces the register (doléances, subjects and their
other spellings, historical passages) and leaves groups and actions alone.
Restoring onto an installation that already holds doléances discards those
doléances, so do it on a fresh installation.

A restore is all or nothing. If it fails, the answer says so and nothing has
changed.

*Full* packages, which also carry groups, actions and the audit log, are
refused unless the backend runs with `DOLEANCES_SNAPSHOT_FULL_ENABLED=true`.
Only console admins can produce one, and you will not find one on a public
Archive page.

### Publish your own archive

A restore loads data but does not create a downloadable archive. To make your
copy offer one on its own Archive page, as every installation should, generate
one on the console's Snapshots page, or ask the backend directly:

```sh
curl -f -X POST -H 'Content-Type: application/json' \
  -d '{"kind":"contributions","notes":"after initial import"}' \
  http://localhost:7300/v1/snapshots
```

## 4. Stop, restart, start over

```sh
docker compose -f docs/docker-compose.yml --profile console down       # stop; data is kept
docker compose -f docs/docker-compose.yml --profile console up -d      # start again
docker compose -f docs/docker-compose.yml --profile console down -v    # stop AND delete all data
```

The data lives in two named volumes: `database` (PostgreSQL) and `backend`
(stored snapshots). With the binaries, it is the `doleances.db` file and the
`storage/` directory: stop the three processes with Ctrl-C, and delete those
two to start over.

## What this setup leaves out

- **Real curator sign-in.** The console here runs with `--development`. A
  deployment where other people reach the console signs curators in through
  Authentik, which is a deployment of its own; see
  [authentik-console-auth.md](authentik-console-auth.md).
- **TLS.** Put a reverse proxy in front of port `8201` before exposing it.
  Passkeys only work on `https://` or on `localhost`.
- **Map tiles and reverse geocoding** use the public OpenStreetMap services by
  default, and their usage policies forbid heavy use. See
  [geocoding-providers.md](geocoding-providers.md) before going live.
