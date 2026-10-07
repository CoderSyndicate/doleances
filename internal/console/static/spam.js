/*
 * The dropped submissions.
 *
 * What the classifier refused on its own, kept briefly so somebody can check
 * the filter is not eating real doléances. Rescuing sends one to the curation
 * queue — it never publishes, because a glance at a sampling page should not
 * put text in front of the public.
 */
(function () {
  "use strict";

  var strings = window.spamStrings || {};
  var status = document.getElementById("status");
  var held = document.getElementById("held");
  var list = document.getElementById("items");
  var retention = document.getElementById("spam_retention_hours");

  function say(message) {
    status.textContent = message;
  }

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

  function when(iso) {
    var date = new Date(iso);
    return isNaN(date) ? iso : date.toLocaleString();
  }

  function card(item) {
    var card = element("article", "card");

    var meta = element("div", "card-meta");

    // Contested first, and said first. A reader on the public page thought
    // this refusal was wrong and asked for somebody to look — the backend
    // sorts those to the top of this listing, and the pill is what says why
    // one is up here. It is a request for attention and not a verdict: the
    // count moves a submission up a page and decides nothing.
    if (item.pleas > 0) {
      meta.appendChild(element("span", "pill contested",
        strings.contested.replace("{count}", item.pleas)));
    }

    // A duplicate was never scored, so a confidence of 0 beside it would read
    // as "the classifier hated this" when no classifier ever saw it. Say which
    // of the two reasons put this here, and say only that one.
    if (item.refusal === "duplicate" || item.duplicate_of) {
      meta.appendChild(element("span", "pill warn", strings.duplicate));
    } else if (item.refusal === "payload") {
      meta.appendChild(element("span", "pill warn", strings.payload));
    } else if (item.refusal) {
      // A named refusal — threat, contact, identifies. The score is shown too:
      // these are the cases where a high score and a refusal sit together, and
      // hiding the score would hide that the model judged the writing genuine.
      meta.appendChild(element("span", "pill warn", strings.refused + " " + item.refusal));
      meta.appendChild(element("span", null, strings.confidence + " " + item.confidence));
    } else {
      meta.appendChild(element("span", null, strings.confidence + " " + item.confidence));
    }
    if (item.language) {
      meta.appendChild(element("span", null, item.language));
    }
    meta.appendChild(element("span", null, strings.expires + " " + when(item.expires_at)));
    card.appendChild(meta);

    card.appendChild(element("p", "card-text", item.text));

    // The point of this page is noticing the filter eating real doléances,
    // and a score cannot be argued with. The model's sentence can.
    if (item.reason) {
      var box = element("div", item.refusal ? "reason refused" : "reason");
      box.appendChild(element("span", "reason-label", strings.reasonLabel));
      box.appendChild(document.createTextNode(item.reason));
      card.appendChild(box);
    }

    var actions = element("div", "theme-actions");

    var rescue = element("button", "button small", strings.rescue);
    rescue.addEventListener("click", function () {
      act("/api/spam/" + encodeURIComponent(item.id) + "/rescue", "POST", strings.rescued);
    });
    actions.appendChild(rescue);

    var remove = element("button", "button small danger", strings.remove);
    remove.addEventListener("click", function () {
      if (window.confirm(strings.confirmDelete)) {
        act("/api/spam/" + encodeURIComponent(item.id), "DELETE", "");
      }
    });
    actions.appendChild(remove);

    card.appendChild(actions);
    return card;
  }

  function act(url, method, message) {
    fetch(url, { method: method, credentials: "same-origin" })
      .then(function (response) {
        if (!response.ok) {
          throw new Error(strings.failed + " " + response.status);
        }
        if (message) {
          say(message);
        }
        return load();
      })
      .catch(function (error) {
        say(error.message);
      });
  }

  // Which page of the sample is on screen. The sample is purged, so it is
  // ordered oldest first — the one nearest its deletion is the one nobody
  // gets another chance to look at — with anything a reader contested above
  // all of it.
  var page = 1;

  function load() {
    return fetch("/api/spam?page=" + page, { credentials: "same-origin" })
      .then(function (response) {
        return response.json();
      })
      .then(function (sample) {
        if (sample.error) {
          throw new Error(sample.error);
        }
        retention.value = sample.retention_hours;
        held.textContent = strings.heldCount + " " + sample.held;

        list.textContent = "";
        var items = sample.items || [];
        showPager(sample, items.length);
        if (!items.length) {
          list.appendChild(element("p", "empty", strings.empty));
          return;
        }
        items.forEach(function (item) {
          list.appendChild(card(item));
        });
      })
      .catch(function (error) {
        say(strings.failed + " " + error.message);
      });
  }

  document.getElementById("retention-form").addEventListener("submit", function (event) {
    event.preventDefault();
    say(strings.saving);

    fetch("/api/curation", {
      method: "PUT",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ spam_retention_hours: Number(retention.value) })
    })
      .then(function (response) {
        return response.json().then(function (body) {
          if (!response.ok) {
            throw new Error(strings.failed + " " + (body.error || response.status));
          }
          say(strings.saved);
          return load();
        });
      })
      .catch(function (error) {
        say(error.message);
      });
  });

  load();
  // A full page is the only evidence of a next one that does not cost a count
  // over the whole sample on every request.
  function showPager(sample, shown) {
    var pager = document.getElementById("spam-pager");
    if (!pager) {
      return;
    }
    var perPage = sample.per_page || 25;
    var isLast = shown < perPage;

    document.getElementById("spam-position").textContent = sample.page || page;
    document.getElementById("spam-prev").disabled = (sample.page || page) <= 1;
    document.getElementById("spam-next").disabled = isLast;
    pager.hidden = (sample.page || page) <= 1 && isLast;
  }

  var prev = document.getElementById("spam-prev");
  var next = document.getElementById("spam-next");
  if (prev && next) {
    prev.addEventListener("click", function () {
      if (page > 1) {
        page--;
        load();
      }
    });
    next.addEventListener("click", function () {
      page++;
      load();
    });
  }

})();
