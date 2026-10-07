/*
 * LLM service settings.
 *
 * The API key is write-only: the backend never returns it, so the field starts
 * empty and an empty field means "keep the stored key". That is what lets the
 * other settings be edited without re-entering the credential.
 */
(function () {
  "use strict";

  var strings = window.llmStrings || {};
  var form = document.getElementById("llm-form");
  var status = document.getElementById("status");
  var models = document.getElementById("models");
  var keyState = document.getElementById("key-state");

  var FIELDS = [
    "base_url",
    "classification_model",
    "translation_model",
    "timeout_seconds",
    "accept_threshold",
    "curate_threshold"
  ];

  function say(message) {
    status.textContent = message;
  }

  function field(name) {
    return document.getElementById(name);
  }

  function fill(settings) {
    FIELDS.forEach(function (name) {
      if (settings[name] !== undefined && settings[name] !== null) {
        field(name).value = settings[name];
      }
    });
    keyState.textContent = settings.api_key_set ? strings.keySet : strings.keyUnset;
  }

  function payload() {
    var body = {
      base_url: field("base_url").value.trim(),
      classification_model: field("classification_model").value.trim(),
      translation_model: field("translation_model").value.trim(),
      timeout_seconds: Number(field("timeout_seconds").value),
      accept_threshold: Number(field("accept_threshold").value),
      curate_threshold: Number(field("curate_threshold").value)
    };
    // Only send a key when one was typed: an empty value means "unchanged",
    // never "clear it". Clearing is a separate, deliberate button.
    var key = field("api_key").value;
    if (key) {
      body.api_key = key;
    }
    return body;
  }

  function failed(response, body) {
    return strings.failed + " " + ((body && body.error) || response.status);
  }

  function load() {
    return fetch("/api/llm", { credentials: "same-origin" })
      .then(function (response) {
        return response.json();
      })
      .then(function (settings) {
        if (settings.error) {
          throw new Error(settings.error);
        }
        fill(settings);
      })
      .catch(function (error) {
        say(strings.failed + " " + error.message);
      });
  }

  form.addEventListener("submit", function (event) {
    event.preventDefault();
    say(strings.saving);

    fetch("/api/llm", {
      method: "PUT",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload())
    })
      .then(function (response) {
        return response.json().then(function (body) {
          if (!response.ok) {
            throw new Error(failed(response, body));
          }
          // Do not keep the typed key in the DOM once it is stored.
          field("api_key").value = "";
          say(strings.saved);
          return load();
        });
      })
      .catch(function (error) {
        say(error.message);
      });
  });

  document.getElementById("clear-key").addEventListener("click", function () {
    if (!window.confirm(strings.confirmClear)) {
      return;
    }
    fetch("/api/llm/key", { method: "DELETE", credentials: "same-origin" })
      .then(function (response) {
        if (!response.ok) {
          throw new Error(failed(response, null));
        }
        field("api_key").value = "";
        return load();
      })
      .catch(function (error) {
        say(error.message);
      });
  });

  document.getElementById("test").addEventListener("click", function () {
    say(strings.testing);
    models.textContent = "";

    fetch("/api/llm/test", { method: "POST", credentials: "same-origin" })
      .then(function (response) {
        return response.json().then(function (body) {
          if (!response.ok) {
            throw new Error(failed(response, body));
          }
          say(body.message);
          (body.models || []).forEach(function (name) {
            var chip = document.createElement("span");
            chip.className = "subject";
            chip.textContent = name;
            models.appendChild(chip);
          });
        });
      })
      .catch(function (error) {
        say(error.message);
      });
  });

  load();
})();
