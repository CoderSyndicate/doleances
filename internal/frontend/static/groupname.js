// Live availability check for a group name.
//
// A group name is unique by construction — it is how a group is addressed and
// how two groups in the same town are told apart — so the form asks before the
// submit rather than rejecting a filled-in page afterwards. The real check is
// the write; this one only saves somebody the round trip.
//
// No-ops on every page that has no such field, which is why it can be loaded
// once from the layout like the subject filter.
(function () {
  "use strict";

  var DEBOUNCE_MS = 400;

  function attach(field) {
    var status = document.getElementById("name-status");
    if (!status) {
      return;
    }

    var endpoint = field.dataset.nameCheck;
    var timer = null;
    var pending = null;

    function say(text, taken) {
      status.textContent = text;
      status.classList.toggle("taken", !!taken);
      status.classList.toggle("free", !!text && !taken);
    }

    function check() {
      var name = field.value.trim();
      if (name.length < 2) {
        say("");
        return;
      }

      // Only the latest answer is allowed to speak: typing is faster than the
      // network, and an early reply landing after a later one would label the
      // name on screen with the verdict on a name that is no longer there.
      var mine = name;
      pending = mine;

      fetch(endpoint + "?name=" + encodeURIComponent(name), {
        headers: { "Accept": "application/json" }
      })
        .then(function (response) {
          return response.ok ? response.json() : null;
        })
        .then(function (payload) {
          if (pending !== mine || !payload) {
            return;
          }
          say(payload.available ? field.dataset.free : field.dataset.taken, !payload.available);
        })
        .catch(function () {
          // The backend being unreachable is not the writer's problem, and a
          // red warning here would read as a verdict on their name.
          if (pending === mine) {
            say("");
          }
        });
    }

    field.addEventListener("input", function () {
      say("");
      window.clearTimeout(timer);
      timer = window.setTimeout(check, DEBOUNCE_MS);
    });
  }

  document.addEventListener("DOMContentLoaded", function () {
    var fields = document.querySelectorAll("[data-name-check]");
    for (var i = 0; i < fields.length; i++) {
      attach(fields[i]);
    }
  });
})();
