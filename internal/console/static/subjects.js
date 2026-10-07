/*
 * The subject vocabulary.
 *
 * Renaming changes the word a reader sees and nothing else: the slug stays,
 * because it is the stable identity and appears in the query strings people
 * have bookmarked, and the match key stays, because dropping it would make the
 * next doléance using the old word a second row. A curator who wants two
 * subjects to become one merges them on the curation queue instead.
 */
(function () {
  "use strict";

  var strings = window.subjectStrings || {};
  var status = document.getElementById("status");
  var list = document.getElementById("vocabulary");
  var search = document.getElementById("search");

  // Which listing is on screen, so a rename reloads the same one rather than
  // dropping the curator back to the full vocabulary.
  var source = "/api/curation/vocabulary";
  var note = "";

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

  function rename(subject) {
    var label = window.prompt(strings.renamePrompt, subject.label);
    // A cancelled prompt is a cancelled decision, and an unchanged one is not
    // a decision at all.
    if (label === null || label.trim() === "" || label === subject.label) {
      return;
    }
    var reason = window.prompt(strings.reasonPrompt, "");
    if (reason === null) {
      return;
    }

    say(strings.working);
    fetch("/api/curation/subjects/" + encodeURIComponent(subject.id) + "/rename", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ label: label, reason: reason })
    })
      .then(function (response) {
        if (!response.ok) {
          // A label of more than three words is refused by the backend. That
          // is an answer to the curator, so it is shown as one.
          return response.text().then(function (detail) {
            throw new Error(detail || response.statusText);
          });
        }
        say(strings.renamed);
        load();
      })
      .catch(function (err) {
        say(strings.failed + " " + err.message);
      });
  }

  function card(subject) {
    var card = element("article", "card");

    var meta = element("div", "card-meta");
    meta.appendChild(element("strong", null, subject.label));
    if (subject.language) {
      meta.appendChild(element("span", "pill", subject.language));
    }

    // Usage first, because it is what makes this a decision: a label on three
    // hundred doléances is a filter people are using.
    if (subject.messages > 0) {
      meta.appendChild(element("span", null,
        strings.messages.replace("{count}", subject.messages)));
    } else {
      meta.appendChild(element("span", "pill warn", strings.unused));
    }

    if (subject.qid) {
      var entity = element("a", "pill", subject.qid);
      entity.href = subject.entity;
      entity.target = "_blank";
      entity.rel = "noreferrer noopener";
      entity.title = strings.entity;
      meta.appendChild(entity);
    }
    card.appendChild(meta);

    // The other spellings the register already recognises. A curator renaming
    // a subject should see that the word disappearing from view is still how
    // incoming doléances are matched.
    var aliases = subject.aliases || [];
    if (aliases.length) {
      var names = aliases.map(function (alias) {
        return alias.language ? alias.language + ": " + alias.label : alias.label;
      });
      card.appendChild(element("p", "section-note",
        strings.aliases.replace("{count}", aliases.length) + " " + names.join(" · ")));
    }

    if (!subject.qid) {
      card.appendChild(element("p", "section-note", strings.noEntity));
    }

    var bar = element("div", "theme-bar");

    var open = element("a", "button", strings.open);
    open.href = "/subjects/" + encodeURIComponent(subject.id);
    bar.appendChild(open);

    var button = element("button", "button secondary", strings.rename);
    button.type = "button";
    button.addEventListener("click", function () {
      rename(subject);
    });
    bar.appendChild(button);
    card.appendChild(bar);

    return card;
  }

  function render(vocabulary) {
    var subjects = (vocabulary && vocabulary.subjects) || [];

    list.textContent = "";
    if (note) {
      list.appendChild(element("p", "section-note", note));
    }
    if (!subjects.length) {
      list.appendChild(element("p", "empty",
        source.indexOf("detached") >= 0 ? strings.detachedEmpty : strings.empty));
      return;
    }
    subjects.forEach(function (subject) {
      list.appendChild(card(subject));
    });
  }

  function show(path, heading) {
    source = path;
    note = heading || "";
    load();
  }

  function load() {
    fetch(source)
      .then(function (response) {
        return response.json();
      })
      .then(render)
      .catch(function (err) {
        say(strings.failed + " " + err.message);
      });
  }

  document.getElementById("search-go").addEventListener("click", function () {
    var query = search.value.trim();
    if (query) {
      show("/api/curation/vocabulary/search?q=" + encodeURIComponent(query), "");
    }
  });
  search.addEventListener("keydown", function (event) {
    if (event.key === "Enter") {
      event.preventDefault();
      document.getElementById("search-go").click();
    }
  });

  // The worklist: a subject with no parent and no child is unreachable from
  // any broader filter, so only somebody who already knows its exact name
  // will ever find it.
  document.getElementById("show-detached").addEventListener("click", function () {
    show("/api/curation/vocabulary/detached", strings.detachedNote);
  });
  document.getElementById("show-all").addEventListener("click", function () {
    search.value = "";
    show("/api/curation/vocabulary", "");
  });

  load();
})();
