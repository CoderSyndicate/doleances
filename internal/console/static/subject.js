/*
 * One subject, and the two things a curator changes about it: which Wikidata
 * entity it is, and where it sits in the hierarchy.
 *
 * The entity search offers three answers per candidate rather than one.
 * "Self" says this subject *is* that entity. "Parent" and "Child" say it is
 * related to one — creating that entity as a subject of its own if the
 * register does not already hold it.
 *
 * That third case is what makes a hierarchy possible at all. Measured on a
 * real vocabulary, relations between subjects that already existed connected
 * 5 of 41; the rest had parents that were concepts nobody had written a
 * doléance about yet, so somebody has to be able to add them.
 */
(function () {
  "use strict";

  var strings = window.subjectStrings || {};
  var id = window.subjectId;
  var status = document.getElementById("status");
  var heading = document.getElementById("subject-label");
  var meta = document.getElementById("subject-meta");
  var relations = document.getElementById("relations");
  var entityBox = document.getElementById("entity");
  var candidates = document.getElementById("candidates");
  var search = document.getElementById("wd-search");

  // Filled from the subject, so a search is made in the language the subject
  // is written in rather than the one the curator's browser is set to.
  var language = "";
  var hasEntity = false;

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

  function link(href, text, title) {
    var a = element("a", null, text);
    a.href = href;
    a.target = "_blank";
    a.rel = "noreferrer noopener";
    if (title) {
      a.title = title;
    }
    return a;
  }

  function post(path, body, done) {
    say(strings.working);
    fetch(path, {
      method: body === null ? "DELETE" : "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body || {})
    })
      .then(function (response) {
        if (!response.ok) {
          return response.text().then(function (detail) {
            throw new Error(detail || response.statusText);
          });
        }
        say(done);
        load();
      })
      .catch(function (err) {
        say(strings.failed + " " + err.message);
      });
  }

  function renderRelations(subject) {
    relations.textContent = "";

    [
      { title: strings.parents, list: subject.parents || [] },
      { title: strings.children, list: subject.children || [] }
    ].forEach(function (group) {
      var card = element("article", "card");
      card.appendChild(element("p", "card-question", group.title));

      if (!group.list.length) {
        card.appendChild(element("p", "section-note", strings.none));
        relations.appendChild(card);
        return;
      }

      group.list.forEach(function (other) {
        var row = element("div", "card-meta");

        var open = element("a", null, other.label);
        open.href = "/subjects/" + encodeURIComponent(other.id);
        row.appendChild(open);

        if (other.language) {
          row.appendChild(element("span", null, "(" + other.language + ")"));
        }

        var cut = element("button", "button small secondary", strings.unlink);
        cut.type = "button";
        cut.addEventListener("click", function () {
          post("/api/curation/vocabulary/" + encodeURIComponent(id) +
            "/relations/" + encodeURIComponent(other.id), null, strings.linked);
        });
        row.appendChild(cut);

        card.appendChild(row);
      });
      relations.appendChild(card);
    });
  }

  function renderEntity(subject) {
    entityBox.textContent = "";
    hasEntity = !!subject.qid;

    var card = element("article", "card");
    if (!subject.qid) {
      card.appendChild(element("p", "section-note", strings.noEntity));
      entityBox.appendChild(card);
      return;
    }

    var row = element("div", "card-meta");
    row.appendChild(link(subject.entity, subject.qid));

    // The names this entity gave the subject. They are what the register
    // matches incoming doléances against, so a curator replacing the entity
    // should see how much depends on it.
    var aliases = subject.aliases || [];
    if (aliases.length) {
      row.appendChild(element("span", null,
        strings.aliases.replace("{count}", aliases.length)));
    }

    var drop = element("button", "button small secondary", strings.detach);
    drop.type = "button";
    drop.addEventListener("click", function () {
      if (!window.confirm(strings.detachConfirm)) {
        return;
      }
      post("/api/curation/vocabulary/" + encodeURIComponent(id) + "/entity",
        null, strings.attached);
    });
    row.appendChild(drop);

    card.appendChild(row);

    if (aliases.length) {
      var names = aliases.slice(0, 40).map(function (alias) {
        return alias.language ? alias.language + ": " + alias.label : alias.label;
      });
      card.appendChild(element("p", "section-note", names.join(" · ")));
    }
    entityBox.appendChild(card);
  }

  function attach(candidate, relation) {
    post("/api/curation/vocabulary/" + encodeURIComponent(id) + "/entity", {
      qid: candidate.qid,
      label: candidate.label,
      relation: relation
    }, relation === "self" ? strings.attached : strings.linked);
  }

  function candidateCard(candidate) {
    var card = element("article", "card");

    var head = element("p", "card-question");
    head.appendChild(element("strong", null, candidate.label));
    head.appendChild(document.createTextNode(" "));
    head.appendChild(link(candidate.entity, candidate.qid));
    card.appendChild(head);

    // The description is what decides it. "état d'isolement d'une personne"
    // against "strict form of imprisonment" is the whole question.
    if (candidate.description) {
      card.appendChild(element("p", "card-text", candidate.description));
    }

    var row = element("div", "card-meta");

    // An alias match is the shape of the worst errors: "transports spatiaux"
    // is why spaceflight was once offered for "transports".
    if (candidate.match_type === "alias") {
      row.appendChild(element("span", "pill warn", strings.matchedAlias));
    }
    // Already in the vocabulary under another name — choosing it as a parent
    // links that subject instead of inventing a second row for one concept.
    if (candidate.held) {
      row.appendChild(element("span", "pill",
        strings.held.replace("{subject}", candidate.held)));
    }
    if (candidate.article) {
      row.appendChild(link(candidate.article, strings.read));
    }
    card.appendChild(row);

    var bar = element("div", "theme-bar");

    // One identity per subject, so this is offered only while there is none.
    if (!hasEntity) {
      var self = element("button", "button", strings.self);
      self.type = "button";
      self.addEventListener("click", function () { attach(candidate, "self"); });
      bar.appendChild(self);
    }

    [["parent", strings.asParent], ["child", strings.asChild]].forEach(function (pair) {
      var button = element("button", "button secondary", pair[1]);
      button.type = "button";
      button.addEventListener("click", function () { attach(candidate, pair[0]); });
      bar.appendChild(button);
    });

    card.appendChild(bar);
    return card;
  }

  // Explicit submit, never search-as-you-type: the backend paces its Wikidata
  // calls and a typing curator would trip the rate limit inside a sentence.
  function runSearch() {
    var query = search.value.trim();
    if (!query) {
      return;
    }
    say(strings.searching);
    candidates.textContent = "";

    fetch("/api/curation/wikidata?q=" + encodeURIComponent(query) +
      "&language=" + encodeURIComponent(language))
      .then(function (response) {
        if (!response.ok) {
          return response.text().then(function (detail) {
            throw new Error(detail || response.statusText);
          });
        }
        return response.json();
      })
      .then(function (payload) {
        var items = (payload && payload.candidates) || [];
        say("");
        if (!items.length) {
          candidates.appendChild(element("p", "empty", strings.noCandidates));
          return;
        }
        items.forEach(function (candidate) {
          candidates.appendChild(candidateCard(candidate));
        });
      })
      .catch(function (err) {
        say(strings.failed + " " + err.message);
      });
  }

  function load() {
    fetch("/api/curation/vocabulary/" + encodeURIComponent(id))
      .then(function (response) {
        return response.json();
      })
      .then(function (subject) {
        language = subject.language || "";
        heading.textContent = subject.label || "";

        var parts = [];
        if (subject.language) {
          parts.push("(" + subject.language + ")");
        }
        parts.push(subject.messages > 0
          ? strings.messages.replace("{count}", subject.messages)
          : strings.unused);
        meta.textContent = parts.join("  ·  ");

        renderEntity(subject);
        renderRelations(subject);

        if (!search.value) {
          search.value = subject.label || "";
        }
      })
      .catch(function (err) {
        say(strings.failed + " " + err.message);
      });
  }

  document.getElementById("wd-go").addEventListener("click", runSearch);
  search.addEventListener("keydown", function (event) {
    if (event.key === "Enter") {
      event.preventDefault();
      runSearch();
    }
  });

  load();
})();
