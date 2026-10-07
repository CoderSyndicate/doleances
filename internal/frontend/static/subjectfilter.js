/*
 * Two conveniences on top of the subject filter. Everything the filter must do
 * — open, choose several, submit — is already done by <details> and the
 * checkboxes inside it, so this file is allowed to fail entirely without
 * breaking the page.
 *
 *   1. the summary says what is chosen rather than always "Any subject"
 *   2. it closes on Escape, and when you click away from it
 *
 * Neither is something <details> does on its own, and both are what makes it
 * feel like a dropdown rather than an expander.
 */
(function () {
  "use strict";

  var filters = document.querySelectorAll(".subject-filter-menu");
  if (!filters.length) {
    return;
  }

  Array.prototype.forEach.call(filters, function (menu) {
    var summary = menu.querySelector("[data-subject-summary]");
    if (!summary) {
      return;
    }
    // The server-rendered text is the empty state, and it is the correct
    // wording in the reader's language — so it is kept rather than reinvented.
    var placeholder = summary.textContent;

    function describe() {
      var chosen = menu.querySelectorAll('input[name="subject"]:checked');
      if (!chosen.length) {
        summary.textContent = placeholder;
        return;
      }
      // One choice reads better as itself than as "1 selected".
      if (chosen.length === 1) {
        var label = chosen[0].parentNode.querySelector("span");
        summary.textContent = label ? label.textContent : placeholder;
        return;
      }
      summary.textContent = chosen.length + " · " + placeholder;
    }

    menu.addEventListener("change", describe);
    describe();
  });

  document.addEventListener("keydown", function (event) {
    if (event.key !== "Escape") {
      return;
    }
    Array.prototype.forEach.call(filters, function (menu) {
      if (menu.open) {
        menu.open = false;
        // Focus goes back to the control that was opened, or Escape would
        // drop a keyboard user at the top of the document.
        var summary = menu.querySelector("summary");
        if (summary) {
          summary.focus();
        }
      }
    });
  });

  document.addEventListener("click", function (event) {
    Array.prototype.forEach.call(filters, function (menu) {
      if (menu.open && !menu.contains(event.target)) {
        menu.open = false;
      }
    });
  });
})();
