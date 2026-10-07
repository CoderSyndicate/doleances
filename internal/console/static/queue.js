/*
 * The curation queue.
 *
 * Everything awaiting a human decision, including submissions no classifier
 * has ruled on. If no model is configured or reachable, every doléance lands
 * here — humans are the last line, and a register that stops because a machine
 * is down has stopped for good as far as the person who wrote it is concerned.
 */
(function () {
  "use strict";

  var strings = window.queueStrings || {};
  var status = document.getElementById("status");
  var note = document.getElementById("unassessed-note");
  var list = document.getElementById("items");
  var questionSection = document.getElementById("subject-questions");
  var questionList = document.getElementById("questions");
  var entitySection = document.getElementById("entity-proposals");
  var entityList = document.getElementById("entities");

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

  // The model's sentence, boxed. Built with textContent throughout: this
  // string comes from a model that was just handed arbitrary text by a
  // stranger, so it is data like any other.
  function reasonBox(reason, refused) {
    var box = element("div", refused ? "reason refused" : "reason");
    box.appendChild(element("span", "reason-label", strings.reasonLabel));
    box.appendChild(document.createTextNode(reason));
    return box;
  }

  function when(iso) {
    var date = new Date(iso);
    return isNaN(date) ? iso : date.toLocaleString();
  }

  // Publishing asks for nothing. It is the expected outcome, and a curator who
  // let somebody's words stand has nothing to account for — the audit entry
  // still records who did it and when. Refusing is the act that removes
  // somebody's words, so it is the one that has to be justified and confirmed.
  function decide(item, verb, reason) {
    say(strings.working);
    fetch("/api/curation/" + encodeURIComponent(item.id) + "/" + verb, {
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
    meta.appendChild(element("span", null, item.nickname || strings.anonymous));
    if (item.place) {
      meta.appendChild(element("span", null, item.place));
    }
    if (item.birth_year) {
      meta.appendChild(element("span", null, strings.born + " " + item.birth_year));
    }
    if (item.activity) {
      meta.appendChild(element("span", null, item.activity));
    }
    meta.appendChild(element("span", null, when(item.created_at)));

    // Whether a machine has looked at this is the first thing a curator needs
    // to know: deciding without a score is a different act from confirming one.
    if (item.assessed) {
      meta.appendChild(element("span", "pill", strings.confidence + " " + item.confidence));
    } else {
      meta.appendChild(element("span", "pill warn", strings.notAssessed));
    }
    card.appendChild(meta);

    card.appendChild(element("p", "card-text", item.text));

    // Why the classifier sent this here. A curator is deciding in seconds
    // about a text they have not read twice, and this is the fastest way in —
    // including when it is wrong, which is the most useful thing to see.
    if (item.reason) {
      card.appendChild(reasonBox(item.reason, false));
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
      // A cancelled prompt is a cancelled decision, not an empty reason.
      if (reason === null) {
        return;
      }
      decide(item, "reject", reason);
    });
    bar.appendChild(reject);

    card.appendChild(bar);
    return card;
  }

  function render(queue) {
    var items = queue.items || [];
    showPager(queue, items.length);

    list.textContent = "";
    if (!items.length) {
      note.hidden = true;
      list.appendChild(element("p", "empty", strings.empty));
      return;
    }

    // Saying "nothing has been assessed" is not a detail. A queue that looks
    // normal while no classifier is running hides the fact that every decision
    // here is being made by hand.
    if (queue.unassessed >= items.length) {
      note.textContent = strings.noClassifier;
      note.hidden = false;
    } else if (queue.unassessed > 0) {
      note.textContent = strings.unassessed.replace("{count}", queue.unassessed);
      note.hidden = false;
    } else {
      note.hidden = true;
    }

    items.forEach(function (item) {
      list.appendChild(card(item));
    });
  }

  /*
   * Subject questions.
   *
   * Two labels that may be one subject, never merged by a machine. They sit
   * on this page rather than on a settings page of their own because the
   * doléance that raised the question is what makes it answerable: "are
   * Verkehr and transport the same subject?" is a linguistics exam in the
   * abstract and an easy call next to the text somebody actually wrote.
   */
  function evidence(question) {
    var row = element("div", "card-meta");

    if (question.qid) {
      var entity = element("a", "pill", strings.subjects.wikidata + " " + question.qid);
      entity.href = question.entity;
      entity.target = "_blank";
      entity.rel = "noreferrer noopener";
      entity.title = strings.subjects.entity;
      row.appendChild(entity);
    }

    // The second opinion, and its absence said plainly: an embedding outage
    // must not read as "the vectors found nothing in common".
    if (question.similarity > 0) {
      row.appendChild(element("span", "pill",
        strings.subjects.similarity + " " + question.similarity.toFixed(2)));
    } else {
      row.appendChild(element("span", "pill", strings.subjects.noSimilarity));
    }

    if (question.cross_language) {
      row.appendChild(element("span", "pill", strings.subjects.crossLanguage));
    }

    // The two sides' own identities, when both have one. Stronger than the
    // cosine in either direction, and it points both ways.
    if (question.entities === "same") {
      row.appendChild(element("span", "pill", strings.subjects.sameEntity));
    } else if (question.entities === "different") {
      row.appendChild(element("span", "pill warn", strings.subjects.differentEntities));
    }

    // The most informative thing on the card. Wikidata claiming an identity
    // the vectors see no trace of usually means the search landed on
    // something absurd — English "pension" resolves to a guest house.
    if (question.agreement === "agree") {
      row.appendChild(element("span", "pill", strings.subjects.agree));
    } else if (question.agreement === "disagree") {
      row.appendChild(element("span", "pill warn", strings.subjects.disagree));
    }

    return row;
  }

  function side(subject) {
    var wrap = element("span", "subject-side");
    wrap.appendChild(element("strong", null, subject.label));

    // " (fr)" as plain text, not a pill. A pill is a block of its own and this
    // sits mid-sentence: rendered inline against the label it produced
    // "accès aux soinsfr ist dasselbe Thema wie healthcare accessen", where
    // the one thing the curator has to read is which language each word is in.
    if (subject.language) {
      wrap.appendChild(document.createTextNode(" (" + subject.language + ")"));
    }

    // This side's own entity, which is often the thing that answers the
    // question. Two subjects resolved to one entity are almost certainly one
    // subject; two resolved to different entities are a reason not to merge —
    // and neither showed before, because the card only carried an entity when
    // Wikidata had raised the question.
    if (subject.qid) {
      wrap.appendChild(document.createTextNode(" "));
      var entity = element("a", null, subject.qid);
      entity.href = subject.entity;
      entity.target = "_blank";
      entity.rel = "noreferrer noopener";
      entity.title = strings.subjects.entity;
      wrap.appendChild(entity);
    }
    // Reading the same entity's article in each language is the fastest
    // honest answer: if both describe the same thing, the labels are one
    // subject.
    //
    // Spaced for the same reason as the language above: this sits inside a
    // sentence, in a span with no styling of its own, so nothing separates it
    // from the word before it.
    if (subject.article) {
      wrap.appendChild(document.createTextNode(" "));
      var link = element("a", "subject-article", strings.subjects.read);
      link.href = subject.article;
      link.target = "_blank";
      link.rel = "noreferrer noopener";
      wrap.appendChild(link);
    }
    return wrap;
  }

  function answer(question, verb, reason) {
    say(strings.working);
    fetch("/api/curation/subjects/" + encodeURIComponent(question.id) + "/" + verb, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ reason: reason })
    })
      .then(function (response) {
        if (!response.ok) {
          throw new Error(response.statusText);
        }
        say(verb === "merge" ? strings.subjects.merged : strings.subjects.kept);
        loadQuestions();
      })
      .catch(function (err) {
        say(strings.failed + " " + err.message);
      });
  }

  function questionCard(question) {
    var card = element("article", "card");

    var heading = element("p", "card-question");
    heading.appendChild(side(question.new));
    heading.appendChild(document.createTextNode(" " + strings.subjects.question + " "));
    heading.appendChild(side(question.into));
    card.appendChild(heading);

    card.appendChild(evidence(question));

    if (question.message) {
      card.appendChild(element("p", "section-note", strings.subjects.context));
      card.appendChild(element("p", "card-text", question.message.text));
    } else {
      card.appendChild(element("p", "section-note", strings.subjects.noContext));
    }

    var bar = element("div", "theme-bar");

    // Merging is the destructive answer — it removes a filter people may be
    // using — so it is the one that asks. Keeping two subjects apart changes
    // nothing a reader can see.
    var merge = element("button", "button", strings.subjects.merge);
    merge.type = "button";
    merge.addEventListener("click", function () {
      if (!window.confirm(strings.subjects.confirmMerge)) {
        return;
      }
      var reason = window.prompt(strings.subjects.reasonPrompt, "");
      if (reason === null) {
        return;
      }
      answer(question, "merge", reason);
    });
    bar.appendChild(merge);

    var keep = element("button", "button secondary", strings.subjects.keep);
    keep.type = "button";
    keep.addEventListener("click", function () {
      answer(question, "dismiss", "");
    });
    bar.appendChild(keep);

    card.appendChild(bar);
    return card;
  }

  function renderQuestions(payload) {
    var items = (payload && payload.items) || [];

    questionList.textContent = "";
    // An empty section is hidden rather than shown as "nothing here": these
    // questions are rare by design and a permanent empty heading would train
    // curators to stop reading the page.
    questionSection.hidden = !items.length;

    items.forEach(function (question) {
      questionList.appendChild(questionCard(question));
    });
  }

  function loadQuestions() {
    fetch("/api/curation/subjects")
      .then(function (response) {
        return response.json();
      })
      .then(renderQuestions)
      .catch(function (err) {
        say(strings.failed + " " + err.message);
      });
  }

  /*
   * Wikidata identities awaiting confirmation.
   *
   * No QID is ever attributed without somebody here saying so. An identity
   * decides what a subject is called in fifty languages and which subjects are
   * merged into it, and an unconfirmed pipeline attributed spaceflight to
   * "transports" and solitary confinement to "isolement".
   *
   * The model has already picked from the same list, so the job is usually
   * recognition rather than investigation — which is the only reason a person
   * in this loop is affordable.
   */
  function entityCard(proposal) {
    var card = element("article", "card");

    var heading = element("p", "card-question");
    heading.appendChild(element("span", null, strings.entities.subject + " "));
    heading.appendChild(element("strong", null, proposal.subject));
    heading.appendChild(document.createTextNode(" " + strings.entities.proposes + " "));

    var entity = element("a", null, proposal.label + " (" + proposal.qid + ")");
    entity.href = proposal.entity;
    entity.target = "_blank";
    entity.rel = "noreferrer noopener";
    entity.title = strings.entities.entity;
    heading.appendChild(entity);
    card.appendChild(heading);

    // The description is what decides it: "état d'isolement d'une personne"
    // against "strict form of imprisonment" is the whole question.
    if (proposal.description) {
      card.appendChild(element("p", "card-text", proposal.description));
    }

    var meta = element("div", "card-meta");
    meta.appendChild(element("span", "pill",
      strings.entities.confidence + " " + proposal.confidence));

    // An alias match is the shape of the worst errors: "transports spatiaux"
    // is why spaceflight was ever offered for "transports".
    if (proposal.match_type === "alias") {
      meta.appendChild(element("span", "pill warn", strings.entities.matchedAlias));
    }
    if (proposal.article) {
      var article = element("a", "subject-article", strings.entities.read);
      article.href = proposal.article;
      article.target = "_blank";
      article.rel = "noreferrer noopener";
      meta.appendChild(article);
    }
    card.appendChild(meta);

    if (proposal.reason) {
      card.appendChild(element("p", "section-note", proposal.reason));
    }

    // The doléance the subject came from. "isolement" in a text about a
    // village is a different entity from "isolement" in one about a prison,
    // and this is the only thing that says which.
    if (proposal.message) {
      card.appendChild(element("p", "section-note", strings.entities.context));
      card.appendChild(element("p", "card-text", proposal.message.text));
    }

    var bar = element("div", "theme-bar");

    // Confirming is the act with consequences, so it is the one that asks.
    var confirm = element("button", "button", strings.entities.confirm);
    confirm.type = "button";
    confirm.addEventListener("click", function () {
      if (!window.confirm(strings.entities.confirmPrompt)) {
        return;
      }
      decideEntity(proposal, "confirm");
    });
    bar.appendChild(confirm);

    var reject = element("button", "button secondary", strings.entities.reject);
    reject.type = "button";
    reject.addEventListener("click", function () {
      decideEntity(proposal, "reject");
    });
    bar.appendChild(reject);

    card.appendChild(bar);
    return card;
  }

  function decideEntity(proposal, verb) {
    say(strings.working);
    fetch("/api/curation/entities/" + encodeURIComponent(proposal.id) + "/" + verb, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ reason: "" })
    })
      .then(function (response) {
        if (!response.ok) {
          throw new Error(response.statusText);
        }
        say(verb === "confirm" ? strings.entities.confirmed : strings.entities.rejected);
        loadEntities();
      })
      .catch(function (err) {
        say(strings.failed + " " + err.message);
      });
  }

  function loadEntities() {
    fetch("/api/curation/entities")
      .then(function (response) {
        return response.json();
      })
      .then(function (payload) {
        var items = (payload && payload.items) || [];
        entityList.textContent = "";
        entitySection.hidden = !items.length;
        items.forEach(function (proposal) {
          entityList.appendChild(entityCard(proposal));
        });
      })
      .catch(function (err) {
        say(strings.failed + " " + err.message);
      });
  }

  // Which page of the queue is on screen. Oldest first: a queue is worked
  // from the front, and somebody who wrote a week ago has been waiting a week.
  var page = 1;

  function load() {
    fetch("/api/curation/queue?page=" + page)
      .then(function (response) {
        return response.json();
      })
      .then(render)
      .catch(function (err) {
        say(strings.failed + " " + err.message);
      });
  }

  load();
  loadQuestions();
  loadEntities();
  // A full page is the only evidence of a next one that does not cost a count
  // over the whole queue on every request. One empty page at the exact
  // multiple is a cheaper mistake than counting every time.
  function showPager(queue, shown) {
    var pager = document.getElementById("queue-pager");
    if (!pager) {
      return;
    }
    var perPage = queue.per_page || 25;
    var isLast = shown < perPage;

    document.getElementById("queue-position").textContent = queue.page || page;
    document.getElementById("queue-prev").disabled = (queue.page || page) <= 1;
    document.getElementById("queue-next").disabled = isLast;
    pager.hidden = (queue.page || page) <= 1 && isLast;
  }

  var queuePrev = document.getElementById("queue-prev");
  var queueNext = document.getElementById("queue-next");
  if (queuePrev && queueNext) {
    queuePrev.addEventListener("click", function () {
      if (page > 1) {
        page--;
        load();
      }
    });
    queueNext.addEventListener("click", function () {
      page++;
      load();
    });
  }

})();
