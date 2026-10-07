# doléances

> *Cahiers de doléances 2.0* — a place to write down what is wrong, and to find
> the people nearby who wrote something similar.

## The cahiers de doléances, reopened

Several times since 1789, France has written down its grievances. The people
who wrote them have never once seen the whole. This register makes that view
possible — and it is no longer limited to France alone.

### 1789

The original *cahiers de doléances* were filled in by ordinary people in the
months before the Revolution: what was wrong with their lives, and what they
wanted changed. Their force was in being read together: a thousand private
troubles became one shared condition, and the people carrying it, the
majority. That is how a complaint becomes a political force. They were
collected, carried upward, and they are still readable today.

### 2018

It happened again in the winter of 2018: the gilets jaunes rose, the first
revolt along class lines in decades, and the registers were opened — around
20,000 of them, for some 218,000 contributions.

Two hundred and thirty years apart, the same act: not a political camp with a
programme, but a class writing down what its life had become — reaching for a
register because nothing else represented it.

### What became of them

Published does not mean readable: the 1.9 million contributions sent in online
are open data all right, in multi-gigabyte files untouched since 2020, and the
site that let you browse them returns an error. The town-hall cahiers have not
been sealed since 2025, but reading one means travelling to a departmental
archive and asking for the box. The texts exist; the register does not.

### 2026

It is starting again as you read. On 5 October 2026 the education minister
Édouard Geffray launched *Agora*, an online platform to collect the demands and
worries of school students in the middle of a mobilisation. Whether it is meant
sincerely changes nothing: what is written there belongs to whoever hosts it,
and can be closed, forgotten, left behind a server error, or disappear for any
reason at all. Here, anybody can carry away the whole register — which is the
only thing that makes a text impossible to delete.

### What this register does

There is no shortage of political frustration. What is missing is somewhere to
put it that is neither a comment thread nor a news feed: here every doléance is
placed and attached to subjects, so that what recurs from one place to the next
becomes visible.

This register is open by construction. Everything published downloads in bulk,
with the subjects assigned to it: a corpus already classified, for the people
who wrote it, for the humanities that want to study it, for the movements that
want to use it. The software is open too — so the register can be checked,
copied, rehosted elsewhere. Nothing to release later, nobody to ask for it; not
an open data that is partial, or that comes with no practical means of reading
it.

The difference is not our good intentions, and that is exactly the point. A
platform can be meant sincerely and vanish all the same: a budget, a
reorganisation, a minister who moves on. Here there is nothing to close that is
not already elsewhere — what is written is public the moment it is written,
anybody can carry away all of it, and the software serving it is open. This is
not a promise to behave better than the others; it is a construction in which
the promise is not needed.

In practice:

- **Write a doléance.** A grievance, a proposal, or the story of your life.
  Anonymous by default; a nickname if you want one; a location if you choose to
  share it.
- **Find the others.** A map of local action groups, their meetings, and when
  they next gather.
- **Hear the ones before you.** Historical doléances from several periods and
  regions, translated so anyone can read them, sitting alongside the ones
  written this week.
- **Take a copy.** The whole published register downloads as one archive,
  readable without any tool, ready to rehost.

### What is collected

Nothing is collected without your knowing. Writing asks for no account, no
address, and the site does not follow you. You give only what you choose to
give: a place, a year of birth, an activity, all optional — and the place is
rounded off before it is stored, never an address. What appears, appears
anonymously.

### What is refused

Not everything is published unread. A filter, then people, turn away spam,
harassment and texts that go after people for what they are — racism is not an
opinion, in French law it is an offence, and the same goes for supremacism in
every form. Those texts are not shelved: they are deleted.

The line is simple. Radical views, yes — against a government, an institution,
a policy, immigration: a doléance has every right to be angry, partisan, in the
minority. Against human rights, no. That is the red line, and it does not move.
The checking is automatic and human; the guarantee is that nothing and nobody
can alter your text.

### Who are we?

For now, one person. I work in IT, on systems that have to hold up at scale: it
is my trade, and it is what I know how to do.

What I miss is elsewhere. People talking about what is wrong in their lives
with complete strangers — I believe that is the most important ingredient for
any real change to begin, well ahead of programmes and parties.

That is why I loved the gilets jaunes: complete strangers talked to one
another, made a society of themselves, and voiced a single coherent demand —
the RIC, a citizens' initiative referendum. I would like that same ferment, but
visible to everyone and not confined to France.

The curators — the people who read what the filter could not settle — are
added one by one, through a network of trust: people who share that view. And
the trust is checkable: every decision, to publish or to refuse, is written to
a full audit log with the name of whoever took it.

### Can we be trusted?

That is yours to choose. What we can guarantee lies in what this project does
not do and in what it lets anyone do: it stores no private data; the software
and the data are completely open, downloadable and easy to use; and the code
can be reviewed by anyone who cares to. You do not have to take our word for
it.

And what is refused is public: every text turned away is readable for a few
hours on a page of its own, with the ground it was refused on — and if you
think a refusal is a mistake you can say so, which puts the text in front of a
person.

## Status

**Early construction.** The design is settled and written down; the code is
being built out from the foundation up.
Nothing here runs end to end yet.

## How it works

### A message

A submitted doléance is scored by an LLM on a single question — *is this a
genuine doléance, related to the mission of the project?* — and then:

| confidence | outcome |
|---|---|
| `> 90` | published |
| `> 45` | queued for a human curator |
| `<= 45` | dropped as spam, held briefly where a curator can rescue it |

Curators **accept or reject; they never edit**. Nobody rewrites someone else's
grievance — a register that has been edited is not a record. Every decision is
written to an append-only audit log against the curator's identity.

The author keeps control: the permalink and token issued at submission let them
**edit or delete** their own message later, with no account. An edit re-enters
the same pipeline and only replaces the published text once accepted, so the
original stays visible in the meantime.

### A group

Local action groups are curated the same way and appear on the map only once
accepted. Groups run **actions** — one-time events, or recurrent ones like a
monthly meeting, which people join per occurrence.

Groups are not allowed to quietly die on the map: a recurrent action must be
re-confirmed once a year, and a group that stops being active stops being
shown. Nothing is deleted for going quiet — a hidden group's page still works,
and posting an action brings it back.

### Anonymity

Two identity systems that are deliberately **never joined**:

- **Contributors** are anonymous and hold a per-message token.
- **Group participants** hold an account made of a passkey and a name they
  chose — no email, no phone, nothing anybody verified.

There is no link between them, by design. If one person does both, the system
cannot tell.

## Architecture

Three Go services:

| service | role | exposed |
|---|---|---|
| **backend** | API, persistence, LLM classification and translation | no |
| **console** | administration and curation | yes |
| **frontend** | the public site and the participant pages | yes |

The backend owns the database, the LLM client and every write. The two exposed
services talk to it over its API and hold credentials to nothing else — which
is what lets them ship as static, non-root, read-only, shell-less container
images with almost nothing in them to attack.

Every service is cloud-ready on the same terms: route middleware, a served
OpenAPI spec and docs UI, graceful shutdown, liveness and readiness, and
OpenTelemetry metrics on a **maintenance port** kept separate from the
application port.

### Stack

Go · [Cobra](https://github.com/spf13/cobra) + [Viper](https://github.com/spf13/viper) ·
[zerolog](https://github.com/rs/zerolog) · [Huma v2](https://github.com/danielgtaylor/huma) ·
[GORM](https://gorm.io) · OpenTelemetry · server-rendered `html/template` +
htmx · [Leaflet](https://leafletjs.com) · PostgreSQL in production, SQLite for
development.

Both web UIs are **multilingual from the first commit**, with file-based
export/import so translation never requires touching code.

## Development

Go is the only requirement. `go test ./...` runs the tests, and `go run ./test`
starts all three services locally. The repository layout, the test commands
and the conventions are in [docs/development.md](docs/development.md), the
local launcher in [docs/test-harness.md](docs/test-harness.md); every guide is
listed in [docs/](docs/README.md).

## Design decisions

The full design — the domain model, the curation pipeline, the participation
and notification rules, the privacy and retention commitments — is recorded
beside the code it governs: the reasoning for a decision sits in the comment
above the thing that implements it, and in the tests that pin it. Read that
before proposing a change: most of it is the way it is on purpose.

## Licence

[BSD 3-Clause](LICENSE) — © 2026 CoderSyndicate

## Running it

With Docker Compose or with the binaries alone, and how to load a register
snapshot into a fresh installation from the console: [docs/running.md](docs/running.md).
