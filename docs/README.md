# Documentation

Guides for running, operating and working on doléances. For what the project
is and why it exists, start with the [project README](../README.md).

## Running it

| guide | for |
|---|---|
| [running.md](running.md) | installing the published images with Docker Compose, or running the binaries alone, and loading a register snapshot from the console |
| [docker-compose.yml](docker-compose.yml) | the compose file that guide uses: PostgreSQL, backend, frontend, and an optional console |

## Operating it

| guide | for |
|---|---|
| [theming.md](theming.md) | the theme system: tokens, colourways, the package format, and making and loading a theme |

## Working on it

| guide | for |
|---|---|
| [development.md](development.md) | requirements, build and test commands, the PostgreSQL tests, the repository layout and its conventions |
| [test-harness.md](test-harness.md) | `go run ./test`: the local launcher, its flags, run directories, seeding, real sign-in and the corpus run |

## Ports at a glance

| service | application | maintenance | local harness |
|---|---|---|---|
| backend | 7300, never public | 9300 | 5300 / 5310 |
| console | 8200 | 9200 | 5301 / 5311 |
| frontend | 8201 | 9201 | 5302 / 5312 |

The maintenance port serves `/healthz/live`, `/healthz/ready` and `/metrics`.
Only the application ports of the console and the frontend are meant to be
published.
