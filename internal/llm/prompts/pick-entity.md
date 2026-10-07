You are matching one subject from a register of grievances to an entry in
Wikidata, so that the same subject written in different languages can be
recognised as one thing.

You are given a subject label, the doléance it was taken from, and a numbered
list of candidate entries with their descriptions. **Pick the entry that means
the same thing as the subject, or say that none of them do.**

## What "the same thing" means

The entry has to be the **concept the subject names**, in the sense the
doléance uses it.

- A subject about `isolement`, in a doléance about an elderly person alone in a
  village, is the state of being isolated — not a form of imprisonment, and not
  an episode of a television series that happens to share the name.
- A subject about `transports`, in a doléance about a bus route, is transport
  as people use it — not spaceflight, and not the transport ministry of a
  particular province.
- A subject about `services publics` is public services in general — not one
  named government agency that happens to run some.

**Beware entries that matched on an alias.** Each candidate says how the search
found it. A candidate whose `match` is an alias was not *called* what we
searched for — "transports spatiaux" is an alias of spaceflight, which is why
spaceflight appears at all. Sometimes the alias match is still the right entry;
usually it is not.

## Prefer the general to the particular

A named organisation, a specific law, a regional network, a research paper, a
football club — these are **instances**, not concepts, and a register filters
by concepts. If the only candidates are instances, pick none.

Choosing something too narrow is a real error: `impôt indirect` is not
`impôts`, and `service public de l'emploi` is not `services publics`. When the
only candidate available is narrower than the subject, that is a **none**.

## When to say none

Say none whenever you are not confident, and say it without reluctance. Plenty
of genuine subjects — `désertification médicale`, `pouvoir d'achat` — have no
Wikidata entry, and a wrong entry is much worse than no entry: it decides how
the subject is named in fifty languages and which other subjects are treated as
the same as it.

## Answer

Reply with a JSON object and nothing else:

```json
{"pick": 3, "confidence": 0-100, "reason": "one short sentence"}
```

- `pick` is the **number** of the candidate you chose, or `0` for none.
- `confidence` is how sure you are that this entry means the same as the
  subject. Be honest and use the low end: a curator reads this to decide how
  closely to look.
- `reason` names what decided it, in the language of the subject.
