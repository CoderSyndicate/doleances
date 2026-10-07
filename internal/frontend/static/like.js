// Remembering, in this browser, which doléances it has already said "me too"
// to.
//
// That is the whole of it. There is nobody to attribute a like to — the
// register's readers are anonymous and it wants no identity for them — so the
// server counts presses and this stops the same person pressing twice by
// accident. Somebody determined to press again can, and that is a known and
// accepted property rather than an oversight: the alternative is an identity
// this project refuses to hold.
//
// Everything works without this file. The button is a form post and the count
// comes back from the server; all this adds is the state of having pressed.
(function () {
  "use strict";

  var STORE = "doleances-liked";

  function liked() {
    try {
      return JSON.parse(window.localStorage.getItem(STORE)) || [];
    } catch (e) {
      // Private windows, cleared site data, storage refused outright: none of
      // them are this page's problem, and none should stop it rendering.
      return [];
    }
  }

  function remember(id) {
    try {
      var all = liked();
      if (all.indexOf(id) === -1) {
        all.push(id);
        window.localStorage.setItem(STORE, JSON.stringify(all));
      }
    } catch (e) {
      /* As above. */
    }
  }

  document.addEventListener("DOMContentLoaded", function () {
    var already = liked();
    var buttons = document.querySelectorAll("[data-like]");

    for (var i = 0; i < buttons.length; i++) {
      var button = buttons[i];
      if (already.indexOf(button.dataset.like) !== -1) {
        button.classList.add("pressed");
      }
      button.addEventListener("click", function () {
        // Remembered on the way out rather than on the way back: the form
        // navigates, so there is no "back" to run code in.
        remember(this.dataset.like);
      });
    }
  });
})();
