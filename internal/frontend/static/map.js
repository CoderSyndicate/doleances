/*
 * The map.
 *
 * Modes, all on #map:
 *   data-pick="true"    the contributor places a pin, writing into the hidden
 *                       latitude/longitude fields
 *   data-groups="true"  local groups, clustered as you zoom out
 *   otherwise           the map is built and handed to another script through
 *                       window.doleancesMap, which is how the register page
 *                       drives its own list from the viewport
 *
 * The starting view comes from the country the server inferred from the
 * browser's language. It is deliberately coarse: a country-level view is
 * obviously not where anybody lives, which is what prompts the correction.
 * Nothing is stored until the contributor actually places a pin, and we never
 * ask the browser for its location on our own initiative.
 */
(function () {
  "use strict";

  // Country centres, coarse on purpose. Anything unknown falls back to a view
  // wide enough to be obviously not an answer.
  var COUNTRIES = {
    FR: [46.6, 2.4, 5],
    BE: [50.6, 4.6, 7],
    CH: [46.8, 8.2, 7],
    DE: [51.2, 10.4, 5],
    ES: [40.2, -3.7, 5],
    IT: [42.8, 12.6, 5],
    GB: [54.0, -2.5, 5],
    IE: [53.2, -8.0, 6],
    NL: [52.2, 5.5, 7],
    PT: [39.6, -8.0, 6],
    US: [39.8, -98.6, 4],
    CA: [56.1, -106.3, 3]
  };
  var WORLD = [30.0, 0.0, 2];

  function startingView(code) {
    return COUNTRIES[code] || WORLD;
  }

  function createMap(el) {
    var view = startingView(el.dataset.country);
    var map = L.map(el).setView([view[0], view[1]], view[2]);

    L.tileLayer("https://tile.openstreetmap.org/{z}/{x}/{y}.png", {
      maxZoom: 19,
      // OSM's tile policy requires a Referer and answers "Access blocked"
      // without one — and the site-wide Referrer-Policy: same-origin sends
      // none cross-site. strict-origin sends the site's origin and never the
      // path, so the tile server learns which register is asking, not which
      // doléance somebody is reading.
      referrerPolicy: "strict-origin",
      attribution: '&copy; <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a>'
    }).addTo(map);

    return map;
  }

  // fieldsFor finds the hidden inputs a pin writes into.
  //
  // Scoped to the map's own form rather than looked up by a document-wide id,
  // because a page can now carry several pickers: the group's meeting place
  // and one per action that happens somewhere else. Every picking map is
  // inside exactly one form, and that form owns its own fields.
  function fieldsFor(el) {
    var scope = el.closest("form") || document;
    return {
      lat: scope.querySelector('[data-role="latitude"]'),
      lon: scope.querySelector('[data-role="longitude"]'),
      zoom: scope.querySelector('[data-role="zoom"]')
    };
  }

  function enablePinPicking(map, el, existing) {
    var fields = fieldsFor(el);
    var latField = fields.lat;
    var lonField = fields.lon;
    var zoomField = fields.zoom;
    var marker = null;

    // Nothing to write into is nothing to pick. Better to leave a map that
    // only pans than to record a pin the form will not carry.
    if (!latField || !lonField) {
      return;
    }

    // The zoom travels with the pin because it says what the pin means. A
    // click on the country view is somebody saying "France", not naming their
    // village at random; a click after zooming to a town means the town. The
    // server decides how precise to be from this, so it must reflect what the
    // map actually showed when they chose.
    function recordZoom() {
      if (zoomField) {
        zoomField.value = map.getZoom();
      }
    }

    function place(latlng) {
      if (marker) {
        marker.setLatLng(latlng);
      } else {
        marker = L.marker(latlng, { draggable: true }).addTo(map);
        marker.on("dragend", function () {
          place(marker.getLatLng());
        });
      }
      latField.value = latlng.lat.toFixed(6);
      lonField.value = latlng.lng.toFixed(6);
      recordZoom();
    }

    map.on("click", function (e) {
      place(e.latlng);
    });

    // Zooming after placing the pin is a change of mind about precision, so
    // the last view wins rather than the one that happened to be on screen at
    // the moment of the click.
    map.on("zoomend", recordZoom);

    // A group being edited already has a meeting place, and the map has to
    // open on it. Starting on the country view would ask somebody who came to
    // fix a description to find their own pub again, and an untouched form
    // would then post nothing and leave the pin where it was — which is right,
    // but only because the field still holds it.
    if (existing) {
      map.setView(existing, VENUE_ZOOM);
      place(existing);
    }

    recordZoom();
  }

  // VENUE_ZOOM is building level: a meeting place is stored exactly where the
  // pin is put, so the map has to open close enough to put it on the door.
  var VENUE_ZOOM = 18;

  function showGroups(map, groups) {
    if (!groups.length) {
      return;
    }
    // Clustering is what keeps the map readable when zoomed out.
    var cluster = L.markerClusterGroup();
    groups.forEach(function (group) {
      // Built as nodes rather than an HTML string: a group name is somebody
      // else's text, and this is the one place on the map where it is put
      // into the document.
      var link = document.createElement("a");
      link.href = "/groups/" + encodeURIComponent(group.id);
      link.textContent = group.name;

      var popup = document.createElement("div");
      popup.appendChild(link);
      if (group.place) {
        var place = document.createElement("p");
        place.className = "section-note";
        place.textContent = group.place;
        popup.appendChild(place);
      }

      cluster.addLayer(
        L.marker([group.latitude, group.longitude]).bindPopup(popup)
      );
    });
    map.addLayer(cluster);
  }

  // Exposed so a page that needs its own behaviour on the map does not
  // reimplement the tile layer, the attribution and the country view.
  window.doleancesMap = createMap;

  // whenVisible builds a map the first time its container has a size, and
  // keeps it sized correctly afterwards.
  //
  // An action's map lives inside a panel or a drawer that opens on `:target`,
  // so at load it is `display: none` and measures 0×0. Leaflet laid out in a
  // box with no size draws a grey ruin and stays that way until it is told to
  // measure again — and nothing here knows when a CSS-driven panel opened,
  // because opening it is a fragment the script never sees.
  //
  // A ResizeObserver does know. It fires when the box goes from nothing to
  // something, whichever mechanism opened it: the fragment, the class the
  // server adds to bring a refused draft back, or anything added later.
  //
  // Building lazily also means a page listing six actions pays for the maps
  // somebody actually opens rather than six at once.
  function whenVisible(el, build) {
    var map = null;

    function attempt() {
      if (!el.offsetWidth && !el.offsetHeight) {
        return;
      }
      if (map) {
        map.invalidateSize();
        return;
      }
      map = build();
    }

    if (typeof ResizeObserver === "function") {
      new ResizeObserver(attempt).observe(el);
    }
    // A map that is already on screen must not wait for an observer that only
    // fires on a change.
    attempt();
  }

  function startPicking(el) {
    whenVisible(el, function () {
      var lat = parseFloat(el.dataset.latitude);
      var lon = parseFloat(el.dataset.longitude);
      var existing = isNaN(lat) || isNaN(lon) ? null : L.latLng(lat, lon);

      var map = createMap(el);
      enablePinPicking(map, el, existing);
      return map;
    });
  }

  document.addEventListener("DOMContentLoaded", function () {
    if (typeof L === "undefined") {
      return;
    }

    // Several per page now, so every one of them is picked up rather than the
    // first: a group's meeting place, and one for each action announced
    // somewhere else.
    document.querySelectorAll('[data-pick="true"]').forEach(startPicking);

    var el = document.getElementById("map");
    if (!el) {
      return;
    }
    // Any other page builds its own map through window.doleancesMap; only the
    // group map is driven from here.
    if (el.dataset.groups !== "true") {
      return;
    }

    var map = createMap(el);
    fetch("/api/groups" + window.location.search, { headers: { Accept: "application/json" } })
      .then(function (response) {
        return response.ok ? response.json() : { groups: [] };
      })
      .then(function (payload) {
        showGroups(map, payload.groups || []);
      })
      .catch(function () {
        /* An empty map is a better failure than a broken page. */
      });
  });
})();
