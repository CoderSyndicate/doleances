# The local test harness: `go run ./test`

`test/main.go` starts the whole system on your machine (backend, console and
frontend) with one command, and keeps every launch's logs and data in a
directory of its own. Its code is in `test/harness/`.

It is a development launcher, not a deployment tool. Console authentication is
off by default and the database is SQLite. For a production-shaped setup, see
[running.md](running.md).

```sh
go run ./test
```

On a cold build cache the first start takes a while, because each service is
compiled with `go run`. The harness then prints where everything is:

```
run directory: /…/test/run/2026-10-07-21-14-03

service    application                 maintenance                 docs
backend    http://localhost:5300       http://localhost:5310       http://localhost:5300/docs
console    http://localhost:5301       http://localhost:5311       http://localhost:5301/docs
frontend   http://localhost:5302       http://localhost:5312       http://localhost:5302/docs

logs:      /…/test/run/2026-10-07-21-14-03/logs
databases: /…/test/run/2026-10-07-21-14-03/databases
storage:   /…/test/run/2026-10-07-21-14-03/storage

authentication is DISABLED (--development) — use -sso to exercise it
open localhost, not 127.0.0.1: passkeys refuse an IP address as an origin
press ctrl-c to stop
```

**Open `localhost`, never `127.0.0.1`.** Browsers refuse an IP address as a
passkey's domain, so creating an account fails on `127.0.0.1`. The error
appears only in the browser, so nothing on the server can explain it.

Ctrl-C stops all three services and lets each one finish its graceful
shutdown.

## Flags

| flag | what it does |
|---|---|
| *(none)* | a fresh run: new directory, empty database, fixed ports, console authentication off |
| `-seeding` | once the services answer, fills the run with sample doléances, groups and actions (see [Seeding](#seeding)) |
| `-latest` | carries on with the most recent run instead of starting a new one |
| `-data <run>` | carries on with a named run: its path, its timestamp, or `latest` |
| `-dynamic-ports` | uses free ephemeral ports instead of 53xx, so several runs can be up at once |
| `-log-level <level>` | `trace` (default), `debug`, `info`, … for every service |
| `-sso` | runs the console **with** authentication, to exercise the Authentik setup and sign-in |
| `-superuser` | reopens the console's setup wizard on a run that is already set up. Implies `-sso` |

`-latest` with `-data <some other run>` is refused, because the two
contradict each other.

Examples:

```sh
go run ./test -seeding              # something to look at, straight away
go run ./test -latest               # pick yesterday's run back up
go run ./test -data 2026-10-07-21-14-03
go run ./test -log-level debug      # quieter logs
go run ./test -dynamic-ports        # alongside another run
```

## What it starts

Each service is started with `go run ./cmd/<service>` and these settings:

| | backend | console | frontend |
|---|---|---|---|
| application port | 5300 | 5301 | 5302 |
| maintenance port | 5310 | 5311 | 5312 |
| database | SQLite, in the run directory | none | none |
| storage | the run's `storage/` | none | none |
| `--development` | yes | yes, unless `-sso` | yes |
| `--site-url` | `http://localhost:5302` | `http://localhost:5302` | `http://localhost:5302` |

Only the console reads `--development`, which turns off its sign-in. The
backend and the frontend are given it but do not use it.

The ports are deliberately not the services' defaults (73xx, 82xx, 92xx and
93xx), so a local run never collides with a production-shaped instance on the
same machine, or with another project's services.

### Before starting

- **A previous run still holding the ports is stopped**, politely: it is sent
  SIGTERM so it shuts down cleanly, and only what still holds a port afterwards
  is killed. This needs `lsof`. Without it, you are told the port is busy.
- **Every port is checked** on both the wildcard and the loopback address.
  Otherwise something unrelated listening on `127.0.0.1` could answer the
  run's requests without anybody noticing.

### Starting and stopping

- **Ready means ready.** The harness polls each service's
  `/healthz/ready` on its maintenance port, for up to two minutes.
- **One service exiting ends the session.** Two services out of three is not a
  useful environment, so the others are stopped too.
- **Ctrl-C drains rather than kills.** SIGTERM goes to each service's whole
  process group, because `go run` is the parent of the real binary, and
  signalling `go run` alone would leave the service running. Services get
  45 seconds to drain, then they are killed.

## The run directory

```
test/run/
  2026-10-07-21-14-03/
    logs/
      backend.json        the raw JSON log events
      backend.console     the terminal output, as you saw it
      console.json, console.console, frontend.json, frontend.console
    databases/
      backend.db          the SQLite database
    storage/              snapshots and other generated files
  latest -> 2026-10-07-21-14-03
```

- **Each launch gets its own directory**, named by the second it started, so
  one session never overwrites the last one's logs or data.
- **`test/run/latest`** always points at the run in use, including a resumed
  one, so `tail -f test/run/latest/logs/backend.json` keeps working.
- **The ten most recent runs are kept.** Older ones are deleted when the
  harness starts.
- **To keep a run for good**, mark it: `touch test/run/<run>/KEEP`. A run
  being resumed is never pruned either.
- **`test/run/` is gitignored.**

### Resuming a run

`-latest` or `-data <run>` runs the services **in** that directory, on its
database, storage and logs (logs are appended to). Nothing is copied, so the
register, the curation queue and any accounts you made are just as you left
them.

`-data` accepts whatever is easiest to paste: the full path the harness
printed, the timestamp alone, or `latest`.

### Reading a run

Most of the interesting findings are in the logs and the database, not on the
screen:

```sh
RUN=test/run/latest

# what the services said, most frequent first
grep -oE '"message":"[^"]*"' $RUN/logs/backend.json | sort | uniq -c | sort -rn

# where submissions ended up
sqlite3 $RUN/databases/backend.db \
  "select status, count(*) from messages group by status;"

# the subject vocabulary the classifier built
sqlite3 $RUN/databases/backend.db "select label, language, q_id from subjects;"
```

The logs are at `trace` by default, so they include every request and every
raw model answer.

## Secrets: `test/.env`

The harness reads `test/.env`, if it exists, and passes it to every service.
It prints the names it loaded, never the values. The file is gitignored;
`test/.env.example` is the committed template:

```sh
cp test/.env.example test/.env
```

It holds what should not be on a command line, where it would end up in shell
history and `ps` output. Mainly that is the LLM endpoint and key:

```sh
DOLEANCES_LLM_BASE_URL=https://your-instance/v1
DOLEANCES_LLM_API_KEY=…
```

Without them everything runs, but submissions stay *pending* because nothing
assesses them. Lines may be written as `export NAME=value`, and surrounding
quotes are stripped.

Two settings are worth knowing when seeding a lot:

- `DOLEANCES_GEOCODE_CONTACT`: your address, which Nominatim's usage policy
  asks for.
- `DOLEANCES_GEOCODE_ENABLED=false`: skips reverse geocoding entirely. The
  public Nominatim tolerates very little bulk traffic.

## Seeding

`-seeding` puts **eight doléances, four groups and six actions** into the run,
from [`test/harness/fixtures.json`](../test/harness/fixtures.json):
plausible grievances, and groups that meet.

**It goes through the API**, after the services answer, exactly as a browser
would. Writing fixtures straight into the tables would exercise nothing, and
would keep working long after the real submission path had broken. So every
seeded run also proves that submission, the assessment sweep and the action
pipeline work.

**It signs up for real.** A group needs an admin, and an admin needs an
account. So the seeder runs a real WebAuthn registration with a software
passkey (`passkey.SoftKey`) rather than through a shortcut. That is the long
way round on purpose: registration and sign-in are the most security-sensitive
paths in the backend, and the least visible from a unit test.

What to expect:

- **The doléances reach the register as the classifier gets to them.** Without
  an LLM configured they stay pending.
- **`Le bus ne passe plus le dimanche.` always lands in curation.** It is 33
  characters, under the 100-character floor below which a human reads every
  submission. Approve it from the console's queue.
- **Seeding the same run twice is close to free.** Doléances already in the
  register and group names already taken are skipped, and reported as
  *already there*. A doléance still waiting for the classifier cannot be seen,
  so it is sent again and dropped as a duplicate. That is the anti-flooding
  guard working, not seeding failing.
- **A seeding failure never stops the run.** The services you came for are
  already up, and the count printed at the end shows what happened.

## Exercising real sign-in: `-sso`

By default the console has no sign-in. With `-sso` it authenticates curators
through Authentik, as in production, and the harness prints what you need:

```
console authentication is ON: sign in through authentik
  set it up at   http://localhost:5311/setup   (the console's MAINTENANCE port)
  redirect URI   http://localhost:5301/auth/callback
```

1. Have an Authentik instance, and create an API token in it (*Directory →
   Tokens and App passwords → Create*, intent *API Token*).
2. Open the setup wizard on the console's **maintenance** port, `5311`, and
   give it the instance URL and the token.
3. The wizard creates the OAuth provider, the application, the two roles
   (`<name>_admin` and `<name>_curator`), and your first admin.

The token only needs to live through setup. The wizard mints a non-expiring
token of its own and keeps that one, so Authentik's 30-minute default is fine.

**`-superuser`** reopens the wizard on a run that is already set up, for
example to point it at a different Authentik. Running it again adopts what
already exists rather than recreating it. It implies `-sso`, because the
wizard only matters when sign-in is on.

Combine with `-latest` to repair the sign-in of an existing run without
losing its data:

```sh
go run ./test -latest -superuser
```

## The corpus run

The harness has a second half, `test/harness/corpus.go`, which pushes a corpus
of test submissions through running services against the real model and
checks each outcome: accepted, sent to curation, or dropped. It also acts as a
curator, accepting or rejecting what lands in the queue according to what the
corpus expected. This exercises the audit log and the deletion of rejected
text.

The corpus itself is not in the repository. It contains realistic abuse and
working prompt-injection payloads, so it and the command that runs it
(`.local/tests/`) are gitignored. With them in place:

```sh
go run ./test                  # terminal 1: a fresh run
go run ./.local/tests          # terminal 2: push the corpus through it
```

**Always start from a fresh run.** Submit the corpus twice into one database
and the duplicate guard drops the second pass wholesale, so you would be
measuring the guard rather than the classifier.

## The harness's own tests

`test/harness/` has unit tests like any other package. They cover the
command line each service is given, `.env` parsing, run-directory resolution
and pruning, the fixtures, and seeding. They run with the rest:

```sh
go test ./test/...
```
