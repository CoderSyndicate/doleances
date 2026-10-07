# Theming

How the register's look is defined, and how to make a theme of your own and
load it through the console.

## In short

- **A theme is one design shipped in several colourways**, the way a garment
  comes in colours. Its *structure* (spacing, shape and type) is singular. Its
  *colourways* are named, and there can be many.
- **Everything is a design token**: a named CSS variable such as `--accent` or
  `--radius`. A theme supplies values for those names and nothing else: no CSS,
  no HTML, no JavaScript.
- **A theme package is a zip** of [W3C Design Tokens
  (DTCG)](https://tr.designtokens.org/format/) JSON files, the format Figma and
  Tokens Studio already export.
- **The round trip is:** download a theme from the console, edit the JSON,
  re-zip it, and upload it back.
- **A partial theme is valid.** A colourway that declares one token changes one
  token. Everything it leaves out falls through to the built-in defaults.

## Where themes are managed

Open the console's theme page at **`/settings/theme`** (*Thèmes* in French).
It lists every theme in the library, one row each:

| control | what it does |
|---|---|
| **Upload a theme** | sends a `.zip` package. The theme is stored **and selected at once** |
| **Download the template** | the built-in theme as a package: the place to start a new one |
| colourway dropdown + **Use** | picks the theme and one of its colourways, and makes them active |
| **Images** | the theme's logo, tab icon and sharing image (see [Images](#images-logo-tab-icon-sharing-image)) |
| **Download** | that theme as a package, ready to edit and upload again |
| **Delete** | removes it from the library. The built-in theme cannot be deleted |

Three themes are always there:

- **`built-in`**: the safety-vest design, compiled into the binaries. It is the
  fallback whenever a theme is missing, unreadable or unselected, so it is
  never stored and cannot be deleted.
- **`revolution`**: the tricolore over the paper and ink of the period's
  printed documents, in two colourways, `parchment` and `nuit`.
- **`broadside`**: a denser, serif, square-cornered structure, with one
  colourway, `newsprint`.

The bundled themes live in `internal/theme/library/<name>/`, in exactly the
package layout described below, unzipped. They are copied into the library at
backend startup and never overwrite a stored theme. A bundled theme you delete
comes back at the next restart.

When you switch away from a theme and back, it comes back in the colourway you
last chose for it.

## How it reaches the page

Every page links two stylesheets, in this order:

1. the built-in tokens and base styles, embedded in the binary;
2. **`/theme/tokens.css`**: the active theme rendered as CSS variables.

Because the second comes after the first, it overrides what it declares and
nothing else. The backend renders it from the stored theme. The frontend and
the console cache it for a few seconds and drop the cache as soon as a theme
is selected, so a change shows on the next page load. If the backend cannot be
reached, `/theme/tokens.css` is empty and the pages fall back to the built-in
look rather than losing their colours.

## The package

```
placard.theme.zip
  structure.json          spacing, shape and type: one per theme, fixed name (optional)
  colors/<name>.json      a colourway, named as you like: at least one is required
  assets/…                images and fonts the tokens reference (optional)
  README.md               written into every download; ignored on upload
```

- **The theme's name comes from the file name** when you upload it.
  `.zip`, `.theme`, `.json` and `.dtcg` are stripped, so `placard.theme.zip`
  becomes the theme `placard`. Uploading a file with the same name replaces
  that theme. The name `built-in` is reserved.
- **Only one file is required**: a single `colors/<name>.json`. Without
  `structure.json`, the theme keeps the built-in spacing and type.
- **Anything else at the top level is refused**, with the offending file named.
  macOS archive clutter (`__MACOSX/`, `.DS_Store`) and the `README.md` the
  console writes into every download are skipped, so a downloaded package can
  be uploaded again without cleaning it up first.
- **Uploading replaces; it does not merge.** A colourway or a file missing from
  the new zip stops existing. So edit a downloaded package instead of uploading
  only the parts you changed.

### Limits

| | |
|---|---|
| whole package | 8 MiB |
| one file in `assets/` | 512 KiB |
| files in `assets/` | 64 |
| colourways | 32 |

## Colourways: `colors/<name>.json`

A colourway has two top-level groups, `light` and `dark`, one per mode, with a
`color` token in each:

```json
{
  "light": {
    "accent":    { "$type": "color", "$value": "#0055a4" },
    "on-accent": { "$type": "color", "$value": "#ffffff" }
  },
  "dark": {
    "accent":    { "$type": "color", "$value": "#1a6bc4" },
    "on-accent": { "$type": "color", "$value": "#ffffff" }
  }
}
```

A token present in only one mode takes its other value from the built-in
default.

Readers choose light or dark with the toggle in the page header, or follow
their operating system until they choose. The `dark` values serve both the
system preference and the explicit choice.

### The colour tokens

These are every colour the site uses. A name not in this list is accepted and
written to the stylesheet, but nothing reads it.

| token | used for | built-in light | built-in dark |
|---|---|---|---|
| `bg` | page background | `#f7f9fa` | `#14161a` |
| `surface` | cards, panels, inputs | `#ffffff` | `#22262c` |
| `border` | borders and rules | `#d5dade` | `#3a4049` |
| `text` | body text | `#14161a` | `#f7f9fa` |
| `text-muted` | secondary text | `#5a6169` | `#9aa3ab` |
| `accent` | the masthead band, primary buttons, focus rings, active states | `#e4ff1a` | `#e4ff1a` |
| `accent-deep` | hover and pressed accent | `#c2d913` | `#c2d913` |
| `on-accent` | anything drawn on `accent` | `#14161a` | `#14161a` |
| `stripe-light` | the light band of the stripe motif | `#d9dde0` | `#d9dde0` |
| `stripe-dark` | the dark band of the stripe motif | `#3a4049` | `#3a4049` |
| `paper` | background of historical passages | `#f4efe3` | `#26231d` |
| `paper-text` | text of historical passages | `#2e2a22` | `#e4dcc9` |
| `paper-border` | border of historical passages | `#ddd2ba` | `#423c30` |
| `error` | errors, destructive buttons | `#b3261e` | `#ff8a80` |
| `warn` | warnings | `#8a6100` | `#ffc65c` |
| `good` | confirmations | `#1f6b3a` | `#7ddb9e` |

The authoritative list is [`internal/theme/tokens.css`](../internal/theme/tokens.css).
The template download always matches it.

Two design rules are worth keeping in mind:

- **`on-accent` must be readable on `accent`.** On the vest's fluorescent
  yellow it has to be the dark colour: black on it is about 18:1, white about
  1.1:1. `revolution` turns it the other way round, white on blue.
- **Keep historical passages looking different from today's doléances.** The
  `paper*` tokens exist so that passages from 1789 read as paper and ink beside
  them. A theme that makes the two look alike loses that distinction.

### Contrast is checked, and never blocks

Each colourway is measured against WCAG AA in both modes at upload, and the
console warns about every pairing that falls short:

| foreground on background | minimum |
|---|---|
| `text` on `bg`, `text` on `surface` | 4.5 : 1 |
| `on-accent` on `accent` | 4.5 : 1 |
| `paper-text` on `paper` | 4.5 : 1 |
| `text-muted`, `error`, `warn`, `good` on `bg` | 3 : 1 |

It is advice: a theme that fails it still uploads and can still be selected.
The check reads plain hex values. A colour written any other way (`rgb()`, a
name) is skipped rather than measured.

## Structure: `structure.json`

Structure has no light and dark modes, because a corner radius is the same in
both. It groups tokens by type instead: `dimension`, `fontFamily` and `number`.

```json
{
  "dimension": {
    "radius":       { "$type": "dimension", "$value": "0" },
    "border-width": { "$type": "dimension", "$value": "2px" },
    "font-size":    { "$type": "dimension", "$value": "17px" }
  },
  "fontFamily": {
    "font-ui": {
      "$type": "fontFamily",
      "$value": ["Iowan Old Style", "Palatino", "Georgia", "serif"]
    }
  },
  "number": {
    "line-height":  { "$type": "number", "$value": 1.7 },
    "title-weight": { "$type": "number", "$value": 700 }
  }
}
```

A font stack is a JSON list. Family names containing a space are quoted for you
when the CSS is written.

### The structure tokens

| token | type | used for | built-in |
|---|---|---|---|
| `radius` | dimension | corner rounding | `6px` |
| `border-width` | dimension | border thickness | `1px` |
| `space-xs` … `space-lg` | dimension | the spacing scale | `0.375rem`, `0.75rem`, `1rem`, `2rem` |
| `card-padding-y`, `card-padding-x` | dimension | padding inside a card | `1rem`, `1.125rem` |
| `grid-min` | dimension | narrowest column of a card grid | `280px` |
| `measure` | dimension | the reading column's line length | `68ch` |
| `page` | dimension | width of the page container | `1440px` |
| `gutter` | dimension | space between the content and the window edge | `clamp(1.25rem, 4vw, 3.5rem)` |
| `font-ui` | fontFamily | the interface and today's doléances | `system-ui, -apple-system, "Segoe UI", Roboto, sans-serif` |
| `font-historical` | fontFamily | historical passages | `Georgia, "Iowan Old Style", serif` |
| `font-size` | dimension | base text size | `16px` |
| `line-height` | number | base line height | `1.6` |
| `title-weight` | number | heading weight | `800` |
| `title-tracking` | dimension | heading letter-spacing | `-0.02em` |

A value is written into the stylesheet as given, so any CSS value that is valid
for that property works, `clamp()` and `calc()` included.

## Assets: `assets/…`

A package may carry files under `assets/`, of these types only:

`.svg` `.png` `.webp` `.jpg` `.jpeg` `.gif` `.avif` · `.woff` `.woff2` `.otf` `.ttf`

No HTML, CSS or JavaScript. A file of any other type is refused at upload, with
its name.

A token can point at one with `url()`. The reference must resolve inside the
package:

```
url(assets/paper.webp)          accepted: the package contains it
url(data:image/png;base64,…)    accepted: self-contained, nothing is fetched
url(https://cdn.example/x)      refused: a theme may not reach another server
url(//cdn.example/x)            refused: the same, without a scheme
url(../../etc/passwd)           refused: it climbs out of the package
url(assets/missing.webp)        refused: not in this package
```

An accepted reference is rewritten when the stylesheet is rendered to
`/theme/files/<theme>/<path>`, served by the site itself. This rule is
deliberate: a theme can never make a reader's browser ask a third party for
anything, so it can never hand somebody else the address of every person
reading the register.

> **What this does not yet buy you.** None of the tokens above takes an image,
> and the stylesheets declare no `@font-face`. So a file in `assets/` is
> accepted, stored and served, but no page uses it yet. Font tokens can only
> name fonts the reader's device already has, which is why the bundled themes
> list long fallback stacks. Using packaged images and web fonts needs a code
> change: a token in [`tokens.css`](../internal/theme/tokens.css) that the base
> styles read, or an `@font-face` the overlay renders.

## Images: logo, tab icon, sharing image

These do **not** travel in the package. They are per-theme slots, managed with
the theme's **Images** button:

| slot | where it appears | built-in default |
|---|---|---|
| `logo` | the masthead, beside the site name | a cockade drawn in the text colour |
| `mark` | browser tab, bookmarks, app tiles | the cockade, square |
| `banner` | the image shown when a page is shared | none |

- **Each slot falls back in a fixed order:** the active theme's image, then the
  built-in default, then nothing.
- **Accepted types:** SVG, PNG, WebP, JPEG and GIF, at most 512 KiB each.
- **Replace** uploads one image. **Use default** removes it, so the slot falls
  back.
- **They survive a re-upload.** Uploading a package replaces its contents and
  leaves the images alone, so somebody who changed a colour never removes the
  register's logo by accident.
- **They go with the theme.** Deleting a theme deletes its images.
- **An uploaded SVG is safe to accept.** It is served under a
  Content-Security-Policy that forbids it from running anything, and it is
  never inlined into a page, because an SVG can carry script.

## Making a theme, step by step

1. **Start from something.** On `/settings/theme`, press **Download the
   template** for the built-in design, or **Download** on the theme you want
   to vary. You get a zip with every token filled in and a `README.md`
   explaining the package.

2. **Unzip and rename it.** The file name is the theme name:

   ```sh
   mkdir placard && cd placard
   unzip ../built-in.theme.zip
   ```

3. **Edit the colourways.** Rename `colors/default.json` to something
   meaningful (`colors/jour.json`), change the values, and add more files for
   more colourways. Delete the tokens you do not want to change, and they fall
   through to the built-in values.

4. **Edit or remove `structure.json`.** Delete it to keep the built-in
   spacing and type.

5. **Zip the contents of the folder**, not the folder itself, so that
   `structure.json` and `colors/` sit at the top of the archive:

   ```sh
   zip -r ../placard.theme.zip . -x '.*'
   ```

6. **Upload it** with **Upload a theme**. It is stored and becomes active at
   once, in its first colourway in alphabetical order. Pick another colourway from the dropdown and
   press **Use**. If a contrast pairing fails, the console warns you without
   refusing the upload.

7. **Check both modes.** Toggle light and dark on the public site. To go back,
   press **Use** on `built-in`.

To change it later, **Download** it, edit, and upload the same file name
again. The images stay where they are.

### Without the console

The console forwards to the backend's API, which can also be called directly
on a machine that reaches it (the backend is never public):

```sh
# store a theme and select it
curl -f -X PUT -H 'Content-Type: application/zip' \
  --data-binary @placard.theme.zip \
  'http://localhost:7300/v1/themes/placard?activate=true'

# select a theme and colourway; an empty name restores the built-in theme
curl -f -X PUT -H 'Content-Type: application/json' \
  -d '{"name":"placard","color":"nuit"}' \
  http://localhost:7300/v1/themes/active

# download a theme as a package ("built-in" gives the template)
curl -fo placard.theme.zip http://localhost:7300/v1/themes/placard
```

The upload's answer lists the colourways it found, the number of asset files,
and any contrast warnings. The full API is documented at
`http://localhost:7300/docs`, under *Themes*.

## When an upload is refused

The message names the problem. The common ones:

| message | cause |
|---|---|
| *the package has no colourway* | no file under `colors/` |
| *unexpected file "…"* | a file outside `structure.json`, `colors/` and `assets/`. Often the zip contains the folder rather than its contents (see step 5) |
| *colourway "…" must be a single .json file in colors/* | a subfolder inside `colors/` |
| *DTCG document declares no colour tokens* | a colourway with no `light` or `dark` group |
| *url(…) does not point inside the package* | an external, protocol-relative or climbing reference |
| *url(…) refers to a file the package does not contain* | a typo in an asset path |
| *a theme needs a name of its own* | the file was named `built-in` |
| *the package is … bytes; the limit is …* | over 8 MiB |
