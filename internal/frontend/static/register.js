/*
 * The register, filtered by the map.
 *
 * Panning or zooming the map changes which doléances are listed: the list
 * always describes the area on screen. Move to a valley and you read what
 * people there wrote, which is the whole point of a register organised by
 * place rather than by date.
 *
 * Two things this must not do. It must not hide doléances silently — those
 * pinned no more precisely than a country have no coordinates and can fall
 * inside no viewport, so their number is stated rather than quietly dropped.
 * And it must not fire a request per pixel of panning: the map settles first.
 */
(function () {
  "use strict";

  var strings = window.registerStrings || {};
  var results = document.getElementById("results");
  var count = document.getElementById("count");
  var empty = document.getElementById("empty");
  var subjects = document.getElementById("subjects");

  // Filled on first use from the filter options, which the server rendered in
  // the reader's language.
  var labels = null;

  var map = null;
  var markers = null;
  var pending = null;
  var filtered = false;

  function element(tag, className, text) {
    var el = document.createElement(tag);
    if (className) {
      el.className = className;
    }
    if (text) {
      el.textContent = text;
    }
    return el;
  }

  // One card, the same shape the server renders elsewhere.
  //
  // It is built twice — here and in the message-card template — and that is a
  // real cost: the two can drift, and they did, which is how this page ended
  // up showing cards with no excerpt link and no controls while every other
  // page had them. They are kept together by hand, and a change to one is a
  // change to both.
  //
  // Built with createElement and textContent throughout. Everything in a card
  // is a stranger's words.
  function card(item) {
    var card = element("article", "card message-card");

    var meta = element("div", "card-meta");
    meta.appendChild(element("span", null, item.nickname || strings.anonymous));
    if (item.place) {
      meta.appendChild(element("span", null, item.place));
    }
    meta.appendChild(element("span", null, item.date));
    card.appendChild(meta);

    // The excerpt is the link, so a reader following it can see what they are
    // following — and so the link's accessible name is the doléance's own
    // opening rather than a label invented for it.
    var text = element("p", "card-text");
    var excerpt = element("a", "card-excerpt", item.text);
    excerpt.href = "/doleance/" + encodeURIComponent(item.id);
    if (item.truncated) {
      excerpt.appendChild(element("span", "card-more", "…\u00a0[" + strings.more + "]"));
    }
    text.appendChild(excerpt);
    card.appendChild(text);

    if (item.subjects && item.subjects.length) {
      var list = element("div", "subjects");
      item.subjects.forEach(function (slug) {
        // The API sends slugs, because a slug is the subject's identity and is
        // what the filter queries on. A slug is not a word anybody wrote,
        // though — it is lowercased and stripped of its accents, so "santé"
        // reaches the page as "sante". The filter list beside this one already
        // holds the readable name in the reader's own language, so it is used.
        list.appendChild(element("span", "subject", subjectLabel(slug)));
      });
      card.appendChild(list);
    }

    card.appendChild(actions(item));
    return card;
  }

  // "Me too" and the permalink.
  //
  // voiceCard is a passage from 1789, built to match the "historical-card"
  // template exactly — the same hazard as the doléance card beside it, for the
  // same reason: the map filters this list, so these cards replace the
  // server's, and anything that exists only in the template disappears the
  // moment somebody pans. The two are kept together by hand and a test
  // asserts this file still mentions every piece the template draws.
  function voiceCard(item) {
    var card = element("article", "card historical");

    var header = element("header", "card-header");
    var meta = element("div", "card-meta");
    if (item.period) {
      meta.appendChild(element("span", null, item.period));
    }
    if (item.placeholder) {
      meta.appendChild(element("span", "placeholder-badge", strings.placeholder));
    }
    // The only place the machine translation is admitted. A reader meeting a
    // fluent eighteenth-century grievance deserves to know whose words these
    // are, and the language tag stays after the words for anybody it tells.
    if (item.translated_from) {
      meta.appendChild(element("span", "translated-badge",
        strings.translated + " \u00b7 " + item.translated_from));
    }
    header.appendChild(meta);
    if (item.document_title) {
      header.appendChild(element("p", "card-document", item.document_title));
    }
    card.appendChild(header);

    if (item.title) {
      card.appendChild(element("h3", "card-title", item.title));
    }

    var permalink = "/voices/" + encodeURIComponent(item.id);
    var text = element("p", "card-text");
    var excerpt = element("a", "card-excerpt", item.text);
    excerpt.href = permalink;
    text.appendChild(excerpt);
    card.appendChild(text);

    if (item.region || item.source) {
      var provenance = element("footer", "card-provenance");
      if (item.region) {
        provenance.appendChild(element("p", "card-place", item.region));
      }
      // A passage from somebody who cannot correct the record: the citation
      // is the only thing standing in for them.
      if (item.source) {
        provenance.appendChild(element("p", "card-source", item.source));
      }
      card.appendChild(provenance);
    }

    var footer = element("footer", "card-actions");

    var form = element("form");
    form.method = "post";
    form.action = permalink + "/like";
    var like = element("button", "card-action like");
    like.type = "submit";
    like.setAttribute("data-like", item.id);
    like.setAttribute("data-tip", strings.likeHintPast);
    like.appendChild(icon("heart"));
    like.appendChild(element("span", "like-count", String(item.likes || 0)));
    like.appendChild(element("span", "visually-hidden", strings.like));
    form.appendChild(like);
    footer.appendChild(form);

    if (strings.signedIn) {
      footer.appendChild(keepForm(item, permalink));
    }

    var link = element("a", "card-action", null);
    link.href = permalink;
    link.setAttribute("data-tip", strings.permalink);
    link.appendChild(icon("link"));
    link.appendChild(element("span", "visually-hidden", strings.permalink));
    footer.appendChild(link);

    card.appendChild(footer);
    return card;
  }

  // A real form rather than a fetch, so the control behaves the same here as
  // on every server-rendered card: pressing it posts, the server counts, the
  // page comes back. like.js remembers what this browser pressed.
  function actions(item) {
    var footer = element("footer", "card-actions");
    var permalink = "/doleance/" + encodeURIComponent(item.id);

    var form = element("form");
    form.method = "post";
    form.action = permalink + "/like";

    var like = element("button", "card-action like");
    like.type = "submit";
    // setAttribute rather than dataset.like, so the attribute name appears
    // literally in both files. Somebody renaming it in the template can then
    // grep for it and find this; with `dataset.like` the coupling is real and
    // invisible, which is how these two drifted apart the first time.
    like.setAttribute("data-like", item.id);
    like.setAttribute("data-tip", strings.likeHint);
    like.appendChild(icon("heart"));
    like.appendChild(element("span", "like-count", String(item.likes || 0)));
    like.appendChild(element("span", "visually-hidden", strings.like));
    form.appendChild(like);
    footer.appendChild(form);

    // Keeping it, for a reader who is signed in. Built here as well as in the
    // template for the same reason the rest of the card is: the map filters
    // this list, so these cards replace the server's — and a control that
    // existed only in the template would silently disappear the moment
    // somebody panned the map.
    //
    // Unlike the "me too" beside it, nothing about this is remembered in the
    // browser. A bookmark is a row the register holds for one person who
    // asked for it, so the server is the only place that knows, and what the
    // button shows came from the server with the card.
    if (strings.signedIn) {
      footer.appendChild(keepForm(item, permalink));
    }

    // Asking for a person, or saying one already came. Built here as well as
    // in the template because the map filters this list: a control that
    // existed only server-side would disappear the moment somebody panned.
    footer.appendChild(reportControl(item, permalink));

    var link = element("a", "card-action", null);
    link.href = permalink;
    link.setAttribute("data-tip", strings.permalink);
    link.appendChild(icon("link"));
    link.appendChild(element("span", "visually-hidden", strings.permalink));
    footer.appendChild(link);

    return footer;
  }

  // The mark is a span and the report is a link, which is the difference
  // between a fact and an offer — and the link goes to a page that asks rather
  // than to anything that acts. Nothing on a card may take a published
  // doléance off the register in one press.
  function reportControl(item, permalink) {
    if (item.verified) {
      var mark = element("span", "card-action verified");
      mark.setAttribute("data-tip", strings.verifiedHint);
      mark.appendChild(icon("check"));
      mark.appendChild(element("span", "visually-hidden", strings.verified));
      return mark;
    }

    var ask = element("a", "card-action report");
    ask.href = permalink + "/report";
    ask.setAttribute("data-tip", strings.reportHint);
    ask.appendChild(icon("flag"));
    ask.appendChild(element("span", "visually-hidden", strings.report));
    return ask;
  }

  // Two paths rather than one toggle, exactly as the template does it: a
  // button that says "keep" posts a keep, so a stale card cannot undo what
  // somebody just did.
  function keepForm(item, permalink) {
    var kept = Boolean(item.kept);

    var form = element("form");
    form.method = "post";
    form.action = permalink + (kept ? "/release" : "/keep");

    var button = element("button", "card-action keep" + (kept ? " kept" : ""));
    button.type = "submit";
    button.setAttribute("data-tip", kept ? strings.releaseHint : strings.keepHint);
    button.appendChild(icon(kept ? "bookmarkKept" : "bookmark"));
    button.appendChild(element("span", "visually-hidden", kept ? strings.kept : strings.keep));
    form.appendChild(button);
    return form;
  }

  // The two icons a card carries, drawn rather than fetched: an <img> would be
  // a request per card and an inline string would be markup built from data,
  // which this file does not do anywhere.
  var PATHS = {
    heart: ["M8 13.3S2.5 10 2.5 6.2a2.9 2.9 0 0 1 5.5-1.4 2.9 2.9 0 0 1 5.5 1.4C13.5 10 8 13.3 8 13.3z"],
    link: [
      "M6.6 9.4a2.6 2.6 0 0 0 3.8 0l2-2a2.7 2.7 0 0 0-3.8-3.8l-1 1",
      "M9.4 6.6a2.6 2.6 0 0 0-3.8 0l-2 2a2.7 2.7 0 0 0 3.8 3.8l1-1"
    ],
    bookmark: ["M4 2.5h8v11l-4-3-4 3z"],
    bookmarkKept: ["M4 2.5h8v11l-4-3-4 3z"],
    flag: ["M4 14V2.5h8l-1.5 3L12 8.5H4"],
    check: ["M3 8.5l3.5 3.5L13 5"]
  };

  // The filled bookmark is the same outline with the inside painted: a shape
  // rather than a colour, so "this is in your list" reaches somebody who
  // cannot see the colour difference.
  var FILLED = { bookmarkKept: true };

  function icon(name) {
    var NS = "http://www.w3.org/2000/svg";
    var svg = document.createElementNS(NS, "svg");
    svg.setAttribute("class", "icon");
    svg.setAttribute("viewBox", "0 0 16 16");
    svg.setAttribute("width", "16");
    svg.setAttribute("height", "16");
    svg.setAttribute("aria-hidden", "true");
    svg.setAttribute("focusable", "false");
    svg.setAttribute("fill", FILLED[name] ? "currentColor" : "none");
    svg.setAttribute("stroke", "currentColor");
    svg.setAttribute("stroke-width", "1.5");
    svg.setAttribute("stroke-linecap", "round");
    svg.setAttribute("stroke-linejoin", "round");

    PATHS[name].forEach(function (d) {
      var path = document.createElementNS(NS, "path");
      path.setAttribute("d", d);
      svg.appendChild(path);
    });
    return svg;
  }

  // subjectLabel resolves a slug to the name this reader should see.
  //
  // Built from the filter options, which the server already rendered in the
  // reader's language — so this costs no request and cannot disagree with the
  // list right next to it. A slug with no option falls back to itself: a
  // subject created since the page loaded is better shown as an ugly word than
  // as nothing.
  function subjectLabel(slug) {
    if (!labels) {
      labels = {};
      Array.prototype.forEach.call(
        subjects.querySelectorAll('input[name="subject"]'),
        function (box) {
          var text = box.parentNode.querySelector("span");
          labels[box.value] = text ? text.textContent : box.value;
        });
    }
    return labels[slug] || slug;
  }

  function selectedSubjects() {
    var chosen = [];
    if (!subjects) {
      return chosen;
    }
    Array.prototype.forEach.call(
      subjects.querySelectorAll('input[name="subject"]:checked'),
      function (box) {
        chosen.push(box.value);
      });
    return chosen;
  }

  function query() {
    var params = new URLSearchParams();
    selectedSubjects().forEach(function (slug) {
      params.append("subject", slug);
    });

    if (filtered && map) {
      // One parameter in the order the server parses: north, south, east,
      // west. Four separate ones invited the two sides to disagree silently.
      var b = map.getBounds();
      params.set("bounds", [b.getNorth(), b.getSouth(), b.getEast(), b.getWest()].join(","));
    }
    return params;
  }

  // The map's position lives in the address bar.
  //
  // Without this the register was unreturnable: pan to your commune, read
  // something, reload — and you are back at the whole country with no way to
  // get where you were except by hand. A link somebody sent went to the same
  // nowhere. Any map people actually use keeps this in the URL, and it is the
  // same reason: a view is worth sharing, and a reader should be able to come
  // back to one.
  //
  // Centre and zoom rather than the bounds the request uses, because they are
  // what a browser can restore exactly; the bounds follow from them and the
  // size of the window, which is not the same on the device a link is opened
  // on. Five decimals is about a metre, which is far finer than anybody pans,
  // and it is the reader's own viewport rather than anything a contributor
  // wrote: nothing here is somebody else's location.
  //
  // replaceState, never pushState — panning a map is not navigation, and a
  // back button that walked through every drag would be unusable.
  function rememberView() {
    if (!window.history || !window.history.replaceState) {
      return;
    }
    var params = new URLSearchParams();
    selectedSubjects().forEach(function (slug) {
      params.append("subject", slug);
    });
    if (filtered && map) {
      var centre = map.getCenter();
      params.set("lat", centre.lat.toFixed(5));
      params.set("lng", centre.lng.toFixed(5));
      params.set("z", String(map.getZoom()));
    }
    var search = params.toString();
    window.history.replaceState(null, "", window.location.pathname + (search ? "?" + search : ""));
  }

  // restoreView puts back what rememberView wrote, and reports whether it
  // found a viewport. A link carrying only subjects still filters by them and
  // leaves the map where the country default put it.
  function restoreView() {
    var params = new URLSearchParams(window.location.search);

    if (subjects) {
      var wanted = params.getAll("subject");
      if (wanted.length) {
        Array.prototype.forEach.call(
          subjects.querySelectorAll('input[name="subject"]'),
          function (box) {
            box.checked = wanted.indexOf(box.value) >= 0;
          }
        );
      }
    }

    var lat = parseFloat(params.get("lat"));
    var lng = parseFloat(params.get("lng"));
    var zoom = parseInt(params.get("z"), 10);
    if (!map || isNaN(lat) || isNaN(lng) || isNaN(zoom)) {
      return false;
    }
    map.setView([lat, lng], zoom);
    return true;
  }

  // render draws one list holding both registers.
  //
  // The server sends entries rather than messages, each saying which it is.
  // There used to be a "see everything, even without a place" button here and
  // a note counting what the viewport had hidden; both existed because panning
  // the map silently dropped every doléance that sits on no point. The server
  // keeps slots for those now, so they are always in this list and there is
  // nothing for a button to undo.
  function render(payload) {
    var entries = payload.entries || [];

    results.textContent = "";
    entries.forEach(function (entry) {
      if (entry.kind === "voice" && entry.voice) {
        results.appendChild(voiceCard(entry.voice));
      } else if (entry.message) {
        results.appendChild(card(entry.message));
      }
    });

    // Both of these count doléances rather than cards. A viewport with
    // nothing written in it is still empty when the page has filled itself
    // with passages from 1789, and "24 doléances here" over seven of them and
    // seventeen of those would be a plain untruth.
    var written = payload.messages || 0;

    empty.hidden = written > 0;
    empty.textContent = filtered ? strings.emptyHere : strings.empty;

    if (written === 1) {
      count.textContent = strings.countOne;
    } else {
      count.textContent = strings.count.replace("{count}", written);
    }

    showMarkers(entries);
  }

  // showMarkers puts the listed doléances on the map, clustered.
  //
  // Clustering is not decoration. Locations are coarsened to a cell before they
  // are stored, so everybody who pinned the same quarter shares one point
  // exactly — without clustering their markers sit on top of each other and a
  // town of forty doléances looks like one. The cluster count is the only
  // thing that shows how much was written in a place, which is the question
  // the map is there to answer.
  function showMarkers(entries) {
    if (!map || typeof L === "undefined") {
      return;
    }
    if (markers) {
      map.removeLayer(markers);
    }

    markers = typeof L.markerClusterGroup === "function"
      ? L.markerClusterGroup({
          // A cluster of coincident points cannot be spread apart by zooming,
          // so clicking one has to fan them out instead.
          spiderfyOnMaxZoom: true,
          showCoverageOnHover: false
        })
      : L.layerGroup(); // no cluster plugin: plain pins beat no map at all

    entries.forEach(function (entry) {
      var item = entry.message || entry.voice;
      if (!item || (!item.latitude && !item.longitude)) {
        return; // named a country, or was never placed: sits on no point
      }

      // A passage is drawn as a circle in its own colour rather than as a
      // pin, because it is not a doléance somebody wrote this week and a map
      // that said otherwise would be making this project's central claim by
      // sleight of hand. The colour lives in the stylesheet, under
      // .voice-marker, so it follows the theme like everything else.
      var layer = entry.kind === "voice"
        ? L.circleMarker([item.latitude, item.longitude], {
            radius: 7,
            className: "voice-marker"
          })
        : L.marker([item.latitude, item.longitude]);

      markers.addLayer(layer.bindPopup(popup(entry)));
    });
    markers.addTo(map);
  }

  // popup is the card, in a marker.
  //
  // Built as DOM rather than concatenated markup: a doléance is somebody's own
  // words and may hold anything at all, and string-built HTML would make that
  // an injection into the page.
  function popup(entry) {
    var item = entry.message || entry.voice;
    var wrapper = element("div", "map-popup");

    var meta = element("p", "card-meta");
    if (entry.kind === "voice") {
      meta.appendChild(element("span", null, item.period || ""));
      if (item.region) {
        meta.appendChild(element("span", null, item.region));
      }
    } else {
      meta.appendChild(element("span", null, item.nickname || strings.anonymous));
      if (item.place) {
        meta.appendChild(element("span", null, item.place));
      }
      meta.appendChild(element("span", null, item.date));
    }
    wrapper.appendChild(meta);

    // Enough to recognise it by; the card below the map holds the whole text.
    // The excerpt off the row rather than a second cut with its own length:
    // two truncations for one register is two shapes, and the popup was
    // slicing bytes rather than runes.
    var text = item.text;
    wrapper.appendChild(element("p", "card-text", text));

    return wrapper;
  }

  function load() {
    count.textContent = strings.counting;

    fetch("/api/messages?" + query().toString(), { headers: { Accept: "application/json" } })
      .then(function (response) {
        return response.json();
      })
      .then(render)
      .catch(function () {
        count.textContent = strings.failed;
      });
  }

  // The map settles before anything is fetched. Leaflet fires move events
  // continuously while a drag is in flight, and one request per frame would
  // hammer the backend to render a list nobody has finished choosing yet.
  function scheduleLoad() {
    filtered = true;
    window.clearTimeout(pending);
    pending = window.setTimeout(load, 250);
  }

  document.addEventListener("DOMContentLoaded", function () {
    var el = document.getElementById("map");
    if (el && typeof L !== "undefined" && window.doleancesMap) {
      map = window.doleancesMap(el);
      map.on("moveend", function () {
        rememberView();
        scheduleLoad();
      });
    }

    // A link that carried a viewport means the reader asked for that place,
    // so the list is filtered to it from the first draw rather than showing
    // the world and then jumping.
    if (restoreView()) {
      filtered = true;
    }

    if (subjects) {
      subjects.addEventListener("change", function () {
        rememberView();
        load();
      });
    }
    load();
  });
})();
