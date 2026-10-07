/*
 * Light/dark toggle.
 *
 * The choice is stored per browser and defaults to the operating system until
 * somebody chooses explicitly. The pre-paint snippet in each page's <head> is
 * what prevents a flash of the wrong theme; this file only handles the button.
 */
(function () {
  "use strict";

  var KEY = "doleances-theme";

  function stored() {
    try {
      var value = localStorage.getItem(KEY);
      return value === "light" || value === "dark" ? value : null;
    } catch (e) {
      // Private windows and blocked site data throw rather than return null.
      return null;
    }
  }

  function systemPrefersDark() {
    return window.matchMedia && window.matchMedia("(prefers-color-scheme: dark)").matches;
  }

  function current() {
    return stored() || (systemPrefersDark() ? "dark" : "light");
  }

  function apply(mode) {
    document.documentElement.setAttribute("data-theme", mode);
    try {
      localStorage.setItem(KEY, mode);
    } catch (e) {
      /* The toggle still works for this page view. */
    }
    // The theme manager redraws its previews on this.
    window.dispatchEvent(new CustomEvent("doleances:themechange", { detail: { mode: mode } }));
  }

  function label(button, mode) {
    button.textContent = mode === "dark" ? "☀" : "☾";
    button.setAttribute("aria-pressed", mode === "dark" ? "true" : "false");
  }

  document.addEventListener("DOMContentLoaded", function () {
    var button = document.getElementById("theme-toggle");
    if (!button) {
      return;
    }
    label(button, current());
    button.addEventListener("click", function () {
      var next = current() === "dark" ? "light" : "dark";
      apply(next);
      label(button, next);
    });
  });
})();
