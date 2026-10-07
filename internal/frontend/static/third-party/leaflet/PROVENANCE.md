# Vendored map libraries

- **Leaflet 1.9.4** — `leaflet.js`, `leaflet.css`, `images/*`
  from `https://unpkg.com/leaflet@1.9.4/dist/`
- **Leaflet.markercluster 1.5.3** — `leaflet.markercluster.js`,
  `MarkerCluster.css`, `MarkerCluster.Default.css`
  from `https://unpkg.com/leaflet.markercluster@1.5.3/dist/`

Both are BSD-2-Clause; Leaflet's licence is beside this file and
markercluster carries the same terms.

## Why these are in the repository rather than on a CDN

They were loaded from `unpkg.com` until the register grew credentials. Two
reasons to stop, and the second is what forced it:

**It handed a third party the address of every reader.** A CDN sees the IP of
everybody who loads a page — on a site collecting grievances, at the moment
somebody is reading them. That is the same trade this project refuses
everywhere else: reverse geocoding runs on the backend rather than in the
browser for exactly this reason.

**It is script on the origin where a session cookie lives.** Since accounts
became passkeys, a compromise of that CDN or of either package would run with
the authority of a signed-in reader — and the session cookie being `HttpOnly`
would not help, because a script does not need to read it. It can simply ask
the browser to register another passkey, which the browser will attach the
cookie to. That is persistent account takeover from a supply chain nobody
here controls.

A Content-Security-Policy is what refuses foreign script, and a policy that
allowed `unpkg` would have been no policy at all. So vendoring was the
prerequisite rather than a tidy-up alongside it.

## Updating

Replace the files from the same paths at a new version, update the versions
above, and check the map pages: the marker images are found by Leaflet from
its own script URL, so they must stay in `images/` beside `leaflet.js`.
