/*
 * The group curation queue.
 *
 * A group is judged on different evidence from a doléance — a name, a place
 * and the people who say they will host it — so it gets its own page rather
 * than a section of the message queue.
 *
 * Refusing a group deletes it and the address that proposed it, which is why
 * refusal asks for a confirmation and a reason and acceptance asks for
 * neither.
 */
(function () {
  "use strict";

  var strings = window.groupStrings || {};
  var status = document.getElementById("status");
  var note = document.getElementById("unassessed-note");
  var list = document.getElementById("items");

  if (!list) {
    return;
  }

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

  // Built with textContent throughout. Every string on this page — the name,
  // the description, the model's sentence — was typed by a stranger or written
  // by a model that was handed a stranger's text.
  function reasonBox(reason) {
    var box = element("div", "reason");
    box.appendChild(element("span", "reason-label", strings.reasonLabel));
    box.appendChild(document.createTextNode(reason));
    return box;
  }

  function when(iso) {
    var date = new Date(iso);
    return isNaN(date) ? iso : date.toLocaleString();
  }

  function decide(item, verb, reason) {
    say(strings.working);
    fetch("/api/curation/groups/" + encodeURIComponent(item.id) + "/" + verb, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ reason: reason })
    })
      .then(function (response) {
        if (!response.ok) {
          throw new Error(response.statusText);
        }
        say(verb === "accept" ? strings.accepted : strings.rejected);
        load();
      })
      .catch(function (err) {
        say(strings.failed + " " + err.message);
      });
  }

  function card(item) {
    var card = element("article", "card");

    var meta = element("div", "card-meta");
    meta.appendChild(element("span", null, item.place || strings.noPlace));
    meta.appendChild(element("span", null, when(item.created_at)));
    if (item.assessed) {
      meta.appendChild(element("span", "pill", strings.confidence + " " + item.confidence));
    } else {
      meta.appendChild(element("span", "pill warn", strings.notAssessed));
    }
    card.appendChild(meta);

    card.appendChild(element("h2", "card-title", item.name));
    card.appendChild(element("p", "card-text", item.description || strings.noDescription));

    if (item.reason) {
      card.appendChild(reasonBox(item.reason));
    }

    // Who says they will run it. The addresses are here and nowhere public:
    // this is the one view in the product that shows them, and it is what a
    // curator judges a group on as much as its description.
    if (item.contacts && item.contacts.length) {
      card.appendChild(element("p", "card-source", strings.contacts));
      var people = element("ul", "contacts");
      for (var i = 0; i < item.contacts.length; i++) {
        var contact = item.contacts[i];
        var label = contact.name ? contact.name + " — " + contact.email : contact.email;
        people.appendChild(element("li", null, label));
      }
      card.appendChild(people);
    }

    var bar = element("div", "theme-bar");

    var accept = element("button", "button", strings.accept);
    accept.type = "button";
    accept.addEventListener("click", function () {
      decide(item, "accept", "");
    });
    bar.appendChild(accept);

    var reject = element("button", "button secondary", strings.reject);
    reject.type = "button";
    reject.addEventListener("click", function () {
      if (!window.confirm(strings.confirmReject)) {
        return;
      }
      var reason = window.prompt(strings.reasonPrompt, "");
      if (reason === null) {
        return;
      }
      decide(item, "reject", reason);
    });
    bar.appendChild(reject);

    card.appendChild(bar);
    return card;
  }

  // A change to a group already on the map. Two texts side by side rather
  // than one, because the question is not "is this a good group?" — that was
  // settled — but "is this a good change to that group?".
  function revisionCard(item) {
    var strings2 = strings.revisions || {};
    var card = element("article", "card");

    var meta = element("div", "card-meta");
    meta.appendChild(element("span", null, item.place || item.current_place || strings.noPlace));
    meta.appendChild(element("span", null, when(item.created_at)));
    if (item.assessed) {
      meta.appendChild(element("span", "pill", strings.confidence + " " + item.confidence));
    } else {
      meta.appendChild(element("span", "pill warn", strings.notAssessed));
    }
    // A moved pin is invisible in a label: two points 300 metres apart are
    // both "Düsseldorf", and the map is the thing people walk to.
    if (item.moved) {
      meta.appendChild(element("span", "pill warn", strings2.moved));
    }
    card.appendChild(meta);

    card.appendChild(element("p", "card-source", strings2.was));
    card.appendChild(element("h2", "card-title", item.current_name));
    card.appendChild(element("p", "card-text", item.current_description || strings.noDescription));

    card.appendChild(element("p", "card-source", strings2.becomes));
    card.appendChild(element("h2", "card-title", item.name));
    card.appendChild(element("p", "card-text", item.description || strings.noDescription));

    if (item.reason) {
      card.appendChild(reasonBox(item.reason));
    }

    var bar = element("div", "theme-bar");

    var accept = element("button", "button", strings2.accept);
    accept.type = "button";
    accept.addEventListener("click", function () {
      decideRevision(item, "accept", "");
    });
    bar.appendChild(accept);

    // Refusing an edit is not refusing a group: the group carries on exactly
    // as it is. It still asks, because somebody's words are being discarded.
    var reject = element("button", "button secondary", strings2.reject);
    reject.type = "button";
    reject.addEventListener("click", function () {
      if (!window.confirm(strings2.confirmReject)) {
        return;
      }
      var reason = window.prompt(strings.reasonPrompt, "");
      if (reason === null) {
        return;
      }
      decideRevision(item, "reject", reason);
    });
    bar.appendChild(reject);

    card.appendChild(bar);
    return card;
  }

  function decideRevision(item, verb, reason) {
    var strings2 = strings.revisions || {};
    say(strings.working);
    fetch("/api/curation/group-revisions/" + encodeURIComponent(item.id) + "/" + verb, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ reason: reason })
    })
      .then(function (response) {
        if (!response.ok) {
          throw new Error(response.statusText);
        }
        say(verb === "accept" ? strings2.accepted : strings2.rejected);
        loadRevisions();
      })
      .catch(function (err) {
        say(strings.failed + " " + err.message);
      });
  }

  function loadRevisions() {
    var section = document.getElementById("revision-section");
    var list2 = document.getElementById("revisions");
    if (!section || !list2) {
      return;
    }

    fetch("/api/curation/group-revisions", { headers: { "Accept": "application/json" } })
      .then(function (response) {
        if (!response.ok) {
          throw new Error(response.statusText);
        }
        return response.json();
      })
      .then(function (queue) {
        var items = queue.items || [];
        // Hidden when empty rather than shown as an empty list: most days
        // nothing is waiting, and a permanent empty heading is a heading
        // people stop reading.
        section.hidden = items.length === 0;

        list2.textContent = "";
        for (var i = 0; i < items.length; i++) {
          list2.appendChild(revisionCard(items[i]));
        }
      })
      .catch(function (err) {
        say(strings.failed + " " + err.message);
      });
  }

  function load() {
    fetch("/api/curation/groups", { headers: { "Accept": "application/json" } })
      .then(function (response) {
        if (!response.ok) {
          throw new Error(response.statusText);
        }
        return response.json();
      })
      .then(function (queue) {
        list.textContent = "";
        var items = queue.items || [];

        // How many no classifier ever ruled on. A curator deciding without a
        // score is doing a different job from one confirming one, and the
        // count is how they find out which day this is.
        if (queue.unassessed > 0) {
          note.textContent = strings.unassessed + " " + queue.unassessed;
          note.hidden = false;
        } else {
          note.hidden = true;
        }

        if (!items.length) {
          list.appendChild(element("p", "empty", strings.empty));
          return;
        }
        for (var i = 0; i < items.length; i++) {
          list.appendChild(card(items[i]));
        }
      })
      .catch(function (err) {
        say(strings.failed + " " + err.message);
      });
  }

  // What the groups are doing, waiting on a decision.
  function actionCard(item) {
    var strings2 = strings.actions || {};
    var card = element("article", "card");

    var meta = element("div", "card-meta");
    meta.appendChild(element("span", null, strings2.by + " " + item.group));
    if (item.group_place) {
      meta.appendChild(element("span", null, item.group_place));
    }
    meta.appendChild(element("span", null,
      strings2.when + " " + (item.starts_on || item.recurrence || "—")));
    if (item.type === "recurrent") {
      meta.appendChild(element("span", "pill", strings2.recurring));
    }
    if (item.assessed) {
      meta.appendChild(element("span", "pill", strings.confidence + " " + item.confidence));
    } else {
      meta.appendChild(element("span", "pill warn", strings.notAssessed));
    }
    card.appendChild(meta);

    card.appendChild(element("h2", "card-title", item.title));
    card.appendChild(element("p", "card-text", item.description || strings.noDescription));

    if (item.reason) {
      card.appendChild(reasonBox(item.reason));
    }

    var bar = element("div", "theme-bar");

    var accept = element("button", "button", strings2.accept);
    accept.type = "button";
    accept.addEventListener("click", function () {
      decideAction(item, "accept", "");
    });
    bar.appendChild(accept);

    var reject = element("button", "button secondary", strings2.reject);
    reject.type = "button";
    reject.addEventListener("click", function () {
      if (!window.confirm(strings2.confirmReject)) {
        return;
      }
      var reason = window.prompt(strings.reasonPrompt, "");
      if (reason === null) {
        return;
      }
      decideAction(item, "reject", reason);
    });
    bar.appendChild(reject);

    card.appendChild(bar);
    return card;
  }

  function decideAction(item, verb, reason) {
    var strings2 = strings.actions || {};
    say(strings.working);
    fetch("/api/curation/actions/" + encodeURIComponent(item.id) + "/" + verb, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ reason: reason })
    })
      .then(function (response) {
        if (!response.ok) {
          throw new Error(response.statusText);
        }
        say(verb === "accept" ? strings2.accepted : strings2.rejected);
        loadActions();
      })
      .catch(function (err) {
        say(strings.failed + " " + err.message);
      });
  }

  function loadActions() {
    var section = document.getElementById("action-section");
    var list3 = document.getElementById("queued-actions");
    if (!section || !list3) {
      return;
    }

    fetch("/api/curation/actions", { headers: { "Accept": "application/json" } })
      .then(function (response) {
        if (!response.ok) {
          throw new Error(response.statusText);
        }
        return response.json();
      })
      .then(function (queue) {
        var items = queue.items || [];
        // Hidden when empty, like the revisions: a permanent empty heading is
        // a heading people stop reading.
        section.hidden = items.length === 0;

        list3.textContent = "";
        for (var i = 0; i < items.length; i++) {
          list3.appendChild(actionCard(items[i]));
        }
      })
      .catch(function (err) {
        say(strings.failed + " " + err.message);
      });
  }

  // ---------------------------------------------------------------------
  // Every group, page by page
  // ---------------------------------------------------------------------

  var groupPage = 1;

  // statusCell says what happened to a group and, where there is one, why.
  //
  // The verdict on its own is the least useful thing this table could show. The
  // question this listing exists to answer is "what happened to the group we
  // proposed?", and for a refused one the answer is the model's own sentence —
  // which the backend has been sending all along and nothing rendered.
  //
  // The score comes with it, because a sentence without a number cannot be
  // argued with and the two together are what tell an operator whether a
  // threshold is in the wrong place.
  function statusCell(item) {
    var cell = element("td");
    var strings2 = strings.all || {};

    if (item.status === "accepted") {
      cell.appendChild(element("span", "verified", item.status));
    } else if (item.status === "dropped" || item.status === "rejected") {
      cell.appendChild(element("span", "pill warn", item.status));
    } else {
      cell.appendChild(element("span", "pill", item.status));
    }

    // Said plainly rather than shown as a score of zero: a group nothing has
    // ruled on is a different state from one a model scored badly, and the
    // queue is where an unassessed group is waiting.
    if (!item.assessed) {
      cell.appendChild(element("div", "section-note", strings2.notAssessed));
      return cell;
    }

    if (typeof item.confidence === "number") {
      cell.appendChild(
        element("div", "section-note", strings2.confidence + " " + item.confidence)
      );
    }
    if (item.reason) {
      cell.appendChild(element("div", "section-note", item.reason));
    }
    return cell;
  }

  function groupRow(item) {
    var strings2 = strings.all || {};
    var row = element("tr");

    var name = element("td");
    // The public page, and only for a group that has one. A curator is not an
    // admin of anybody's group, so there is no management view to link to —
    // and a link to a page the frontend answers 404 for would be worse than
    // plain text.
    if (strings.siteBase && item.status === "accepted") {
      var link = element("a", null, item.name);
      link.href = strings.siteBase + encodeURIComponent(item.id);
      link.title = strings2.view;
      name.appendChild(link);
    } else {
      name.appendChild(document.createTextNode(item.name));
    }
    row.appendChild(name);

    row.appendChild(element("td", null, item.place || "—"));
    row.appendChild(statusCell(item));
    row.appendChild(element("td", null, when(item.created_at)));
    return row;
  }

  function loadGroups() {
    var table = document.getElementById("all-groups");
    var pager = document.getElementById("group-pager");
    var position = document.getElementById("groups-position");
    if (!table) {
      return;
    }

    fetch("/api/curation/groups/all?page=" + groupPage, {
      headers: { "Accept": "application/json" }
    })
      .then(function (response) {
        if (!response.ok) {
          throw new Error(response.statusText);
        }
        return response.json();
      })
      .then(function (listing) {
        var strings2 = strings.all || {};
        var groups = listing.groups || [];
        table.textContent = "";

        if (!groups.length) {
          pager.hidden = true;
          var empty = element("tbody");
          var row = element("tr");
          row.appendChild(element("td", "empty", strings2.empty));
          empty.appendChild(row);
          table.appendChild(empty);
          return;
        }

        var head = element("thead");
        var headRow = element("tr");
        [strings2.name, strings2.place, strings2.status, strings2.created]
          .forEach(function (label) {
            headRow.appendChild(element("th", null, label));
          });
        head.appendChild(headRow);
        table.appendChild(head);

        var body = element("tbody");
        for (var i = 0; i < groups.length; i++) {
          body.appendChild(groupRow(groups[i]));
        }
        table.appendChild(body);

        // The count is of every group there is, not of this page: somebody
        // should not have to walk the pages to find out how many there are.
        var first = (listing.page - 1) * listing.per_page + 1;
        var last = first + groups.length - 1;
        position.textContent = first + "\u2013" + last + " / " + listing.total;

        var isLast = last >= listing.total;
        document.getElementById("groups-prev").disabled = listing.page <= 1;
        document.getElementById("groups-next").disabled = isLast;
        pager.hidden = listing.page <= 1 && isLast;
      })
      .catch(function (err) {
        say(strings.failed + " " + err.message);
      });
  }

  var prev = document.getElementById("groups-prev");
  var next = document.getElementById("groups-next");
  if (prev && next) {
    prev.addEventListener("click", function () {
      if (groupPage > 1) {
        groupPage--;
        loadGroups();
      }
    });
    next.addEventListener("click", function () {
      groupPage++;
      loadGroups();
    });
  }

  load();
  loadRevisions();
  loadActions();
  loadGroups();
})();
