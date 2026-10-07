/*
 * Snapshots.
 *
 * The register exports itself so that it does not depend on this installation
 * surviving. Two kinds: the contributions package carries no personal data and
 * is published for anyone to mirror; the full one carries every participant's
 * email address and never leaves this console.
 */
(function () {
  "use strict";

  var strings = window.snapshotStrings || {};
  var status = document.getElementById("status");
  var restoreStatus = document.getElementById("restore-status");
  var list = document.getElementById("items");
  var createForm = document.getElementById("create-form");
  var restoreForm = document.getElementById("restore-form");
  var kindSelect = document.getElementById("snapshot_kind");
  var kindHint = document.getElementById("kind-hint");
  var fullEnabled = false;

  function say(target, message) {
    target.textContent = message;
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

  function size(bytes) {
    if (bytes < 1024) {
      return bytes + " B";
    }
    if (bytes < 1024 * 1024) {
      return Math.round(bytes / 1024) + " KB";
    }
    return (bytes / (1024 * 1024)).toFixed(1) + " MB";
  }

  function summarise(counts) {
    var parts = [];
    Object.keys(counts || {}).sort().forEach(function (key) {
      parts.push(counts[key] + " " + key);
    });
    return parts.join(", ");
  }

  function card(item) {
    var card = element("article", "card");

    var meta = element("div", "card-meta");
    meta.appendChild(element("strong", null, item.tag));
    // The privacy status is the first thing to read, not a detail: these two
    // files are handled completely differently.
    meta.appendChild(element("span", item.public ? "pill" : "pill warn",
      item.public ? strings.publicLabel : strings.personal));
    meta.appendChild(element("span", null, when(item.generated_at)));
    meta.appendChild(element("span", null, size(item.size_bytes)));
    card.appendChild(meta);

    var records = summarise(item.counts);
    if (records) {
      card.appendChild(element("p", "card-text", strings.records + " " + records));
    }
    if (item.notes) {
      card.appendChild(element("p", "card-text", item.notes));
    }

    var bar = element("div", "theme-bar");

    var download = element("a", "button secondary", strings.download);
    download.href = "/snapshots/download/" + encodeURIComponent(item.tag);
    bar.appendChild(download);

    var remove = element("button", "button secondary", strings.remove);
    remove.type = "button";
    remove.addEventListener("click", function () {
      if (!window.confirm(strings.confirmDelete.replace("{tag}", item.tag))) {
        return;
      }
      fetch("/api/snapshots/" + encodeURIComponent(item.tag), { method: "DELETE" })
        .then(function (response) {
          if (!response.ok) {
            throw new Error(response.statusText);
          }
          load();
        })
        .catch(function (err) {
          say(status, strings.failed + " " + err.message);
        });
    });
    bar.appendChild(remove);

    card.appendChild(bar);
    return card;
  }

  function render(catalogue) {
    fullEnabled = catalogue.full_enabled;
    updateKindHint();

    list.textContent = "";
    var snapshots = catalogue.snapshots || [];
    if (!snapshots.length) {
      list.appendChild(element("p", "empty", strings.empty));
      return;
    }
    snapshots.forEach(function (item) {
      list.appendChild(card(item));
    });
  }

  function updateKindHint() {
    if (!kindHint) {
      return;
    }
    if (kindSelect.value === "full") {
      kindHint.textContent = fullEnabled ? strings.hintFull : strings.fullDisabled;
    } else {
      kindHint.textContent = strings.hintContributions;
    }
  }

  function load() {
    fetch("/api/snapshots")
      .then(function (response) {
        return response.json();
      })
      .then(render)
      .catch(function (err) {
        say(status, strings.failed + " " + err.message);
      });
  }

  createForm.addEventListener("submit", function (event) {
    event.preventDefault();
    say(status, strings.creating);

    fetch("/api/snapshots", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        kind: kindSelect.value,
        tag: document.getElementById("snapshot_tag").value.trim(),
        notes: document.getElementById("snapshot_notes").value.trim()
      })
    })
      .then(function (response) {
        return response.json().then(function (body) {
          if (!response.ok) {
            throw new Error(body.error || response.statusText);
          }
          return body;
        });
      })
      .then(function (created) {
        say(status, strings.created.replace("{tag}", created.tag));
        document.getElementById("snapshot_tag").value = "";
        document.getElementById("snapshot_notes").value = "";
        load();
      })
      .catch(function (err) {
        say(status, strings.failed + " " + err.message);
      });
  });

  restoreForm.addEventListener("submit", function (event) {
    event.preventDefault();

    var input = document.getElementById("restore_file");
    if (!input.files.length) {
      say(restoreStatus, strings.restoreNoFile);
      return;
    }
    // A restore replaces what the package covers. Nothing about that is
    // reversible, so it is confirmed rather than merely clicked.
    if (!window.confirm(strings.confirmRestore)) {
      return;
    }

    var data = new FormData();
    data.append("file", input.files[0]);
    say(restoreStatus, strings.restoring);

    fetch("/api/snapshots/restore", { method: "POST", body: data })
      .then(function (response) {
        return response.json().then(function (body) {
          if (!response.ok) {
            throw new Error(body.error || response.statusText);
          }
          return body;
        });
      })
      .then(function (result) {
        say(restoreStatus, strings.restored
          .replace("{kind}", result.kind)
          .replace("{records}", summarise(result.restored)));
        input.value = "";
      })
      .catch(function (err) {
        say(restoreStatus, strings.failed + " " + err.message);
      });
  });

  kindSelect.addEventListener("change", updateKindHint);
  load();
})();
