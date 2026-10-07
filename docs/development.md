# Development

How to build, test and run doléances from a checkout.

## Requirements

**Go**, at the version in [`go.mod`](../go.mod). Nothing else is required:

- SQLite is the development database and needs no setup. The driver is pure Go,
  so there is no cgo and no C toolchain to install.
- Every HTML, CSS and JavaScript file is embedded in the binaries, so there is
  no Node and no front-end build step.

Optional:

- **An OpenAI-compatible LLM endpoint**, for doléances to be classified and
  published. Without one, submissions wait as *pending*. See
  [`test/.env`](#secrets-testenv).
- **Docker**, to run the PostgreSQL tests locally (see below).

## Everyday commands

```sh
go build ./...       # build all three services
go vet ./...         # what CI runs before the tests
go test ./...        # the unit tests
go run ./test        # start all three services locally
```

`go run ./test` is the local test harness. It has its own guide,
[test-harness.md](test-harness.md). In short, it starts the backend, the
console and the frontend together on `localhost:5300`–`5302`, with console
authentication off and a fresh SQLite database. Add `-seeding` to fill it
with sample doléances and groups.

## Tests

### What CI runs

```sh
go mod tidy && git diff --exit-code -- go.mod go.sum   # modules are tidy
go vet ./...
CGO_ENABLED=1 go test -race ./...
```

The race detector needs cgo, so the test step is the one place
`CGO_ENABLED=1` is set. That is about tooling only: the shipped binaries are
built with `CGO_ENABLED=0`, and the SQLite driver is pure Go in both cases.

### Against PostgreSQL

Every other test uses SQLite. A separate job runs the schema migration and a
round trip against a real PostgreSQL, because SQLite accepts column types that
PostgreSQL refuses. That difference once took a deployment down on its first
migration.

To run it locally:

```sh
docker run --rm -d --name doleances-pg -p 5432:5432 \
  -e POSTGRES_USER=doleances -e POSTGRES_PASSWORD=doleances \
  -e POSTGRES_DB=doleances_test postgres:16-alpine

DOLEANCES_TEST_POSTGRES_DSN='postgres://doleances:doleances@127.0.0.1:5432/doleances_test?sslmode=disable' \
  go test -count=1 -run Postgres -v ./internal/store/

docker stop doleances-pg
```

Without `DOLEANCES_TEST_POSTGRES_DSN`, those tests are skipped.

### Beyond unit tests

Unit tests prove the pieces. The failures that matter in this project live in
the wiring between them, such as a field nobody assigned or a column name
that drifted. So the harness can also push a corpus of real submissions
through running services and the real model. See [The corpus
run](test-harness.md#the-corpus-run).

## Secrets: `test/.env`

The harness reads `test/.env` and passes it to every service. It is
gitignored. Start from the committed template:

```sh
cp test/.env.example test/.env
```

The names are the services' own settings: `--llm-api-key` is
`DOLEANCES_LLM_API_KEY`. The one you will usually set is the LLM endpoint and
key. Secrets go in this file rather than on the command line, where they
would end up in shell history and `ps` output.

## Repository layout

```
cmd/<service>/       binaries: wiring only, no application logic
internal/            everything the services are made of
  backend/           the API and every write
  console/           curation and administration UI
  frontend/          the public site
  store/             persistence (GORM)
  llm/prompts/       the prompts sent to the model, as Markdown
  theme/             design tokens and bundled themes
  …
test/                the local harness (go run ./test)
  harness/           its implementation, and the corpus runner
  run/               one directory per launch (gitignored)
build/docker/        the runtime image
docs/                these guides
.github/workflows/   CI and release
```

## Conventions worth knowing before a first change

- **`cmd/` wires, `internal/` implements.** If you would want to unit-test
  it, it does not belong in `main.go`.
- **No markup or styles in Go strings.** Templates, CSS and JS are real files
  in a `static/` or `templates/` folder, embedded with `//go:embed`.
- **No user-visible string is hard-coded.** Both web UIs read every string from
  `internal/<service>/locales/active.<lang>.toml` (English, French, German). A
  new string needs a key in each file.
- **Configuration is flag > environment > config file**, resolved through
  Cobra and Viper. A setting has one name in all three: `--site-url`,
  `DOLEANCES_SITE_URL`, `site-url:`.
- **A commit that changes behaviour comes with its tests.**
- **The reasoning sits next to the code.** Most decisions are explained in
  the comment above the code that implements them, and pinned by a test. Read
  that before changing something that looks odd: it is usually deliberate.
