/*
 * The theme library.
 *
 * A theme is one design shipped with several colourways: one structure —
 * spacing, shape and type — and as many colours as the designer supplied.
 * Each row is a theme: pick its colour from the dropdown, then Use.
 *
 * The built-in default is always listed and can never be deleted, because it
 * is what everything falls back to.
 */
(function () {
  "use strict";

  var strings = window.themeStrings || {};
  var status = document.getElementById("status");
  var list = document.getElementById("themes");

  function say(message) {
    status.textContent = message;
  }

  /*
   * Force the overlay stylesheet to be re-fetched so the page previews the
   * change immediately, rather than after its cache expires.
   */
  function reloadOverlay() {
    document.querySelectorAll('link[href*="/theme/tokens.css"]').forEach(function (link) {
      var url = new URL(link.href, location.href);
      url.searchParams.set("_", Date.now());
      link.href = url.toString();
    });
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

  function swatches(colours) {
    var row = element("div", "swatches");
    ["bg", "surface", "accent", "text"].forEach(function (name) {
      if (!colours || !colours[name]) {
        return;
      }
      var chip = element("span", "swatch");
      chip.style.background = colours[name];
      chip.title = name + ": " + colours[name];
      row.appendChild(chip);
    });
    return row;
  }

  function warningPill(warnings) {
    var pill = element("span", "warnpill", "⚠ " + warnings.length);
    pill.title = warnings
      .map(function (w) {
        return w.mode + ": " + w.pair + " " + w.ratio + " < " + w.min;
      })
      .join("\n");
    return pill;
  }

  /*
   * The colour dropdown. Changing it on the active theme applies at once;
   * on any other theme it only chooses what Use will apply, so a curator can
   * look before switching.
   */
  function colorPicker(item) {
    var select = element("select", "theme-colors");
    select.setAttribute("aria-label", strings.colour);

    (item.colors || []).forEach(function (name) {
      var option = document.createElement("option");
      option.value = name;
      option.textContent = name;
      option.selected = name === item.active_color;
      select.appendChild(option);
    });

    if (item.colors && item.colors.length < 2) {
      select.disabled = true;
    }
    if (item.active) {
      select.addEventListener("change", function () {
        setActive(item.name, select.value);
      });
    }
    return select;
  }

  function failed(response, payload) {
    return strings.failed + " " + ((payload && payload.error) || response.status);
  }

  function row(item) {
    var card = element("article", "card theme-card" + (item.active ? " active" : ""));

    var name = element("div", "theme-name");
    name.appendChild(element("span", null, item.name));
    if (item.built_in) {
      name.appendChild(element("span", "badge builtin", strings.builtIn));
    }
    if (item.active) {
      name.appendChild(element("span", "badge", strings.active));
    }
    if (item.warnings && item.warnings.length) {
      name.appendChild(warningPill(item.warnings));
    }
    card.appendChild(name);
    card.appendChild(swatches(item.swatches));

    var select = colorPicker(item);
    card.appendChild(select);

    var actions = element("div", "theme-actions");

    if (!item.active) {
      var use = element("button", "button small", strings.use);
      use.addEventListener("click", function () {
        setActive(item.name, select.value);
      });
      actions.appendChild(use);
    }

    var assets = element("button", "button small secondary", strings.images);
    assets.addEventListener("click", function () {
      toggleAssets(card, item.name);
    });
    if (item.built_in) {
      assets.disabled = true;
      assets.title = strings.builtInNoAssets;
    }
    actions.appendChild(assets);

    var download = element("a", "button small secondary", strings.download);
    download.href = "/theme/download/" + encodeURIComponent(item.name);
    actions.appendChild(download);

    if (!item.built_in) {
      var remove = element("button", "button small danger", strings.remove);
      remove.addEventListener("click", function () {
        destroy(item.name);
      });
      actions.appendChild(remove);
    }

    card.appendChild(actions);
    return card;
  }

  function setActive(name, colour) {
    say(strings.applying);
    fetch("/api/themes/active", {
      method: "PUT",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ name: name, color: colour })
    })
      .then(function (response) {
        return response.json().then(function (payload) {
          if (!response.ok) {
            throw new Error(failed(response, payload));
          }
          reloadOverlay();
          say(strings.applied + " " + name + (colour ? " · " + colour : ""));
          return load();
        });
      })
      .catch(function (error) {
        say(error.message);
      });
  }

  function destroy(name) {
    if (!window.confirm(strings.confirmDelete + " " + name)) {
      return;
    }
    fetch("/api/themes/" + encodeURIComponent(name), {
      method: "DELETE",
      credentials: "same-origin"
    })
      .then(function (response) {
        if (!response.ok) {
          throw new Error(failed(response, null));
        }
        reloadOverlay();
        return load();
      })
      .catch(function (error) {
        say(error.message);
      });
  }

  /*
   * A theme's images. The named slots — logo, mark, banner — are listed
   * whether filled or not, because an empty slot falls back to the built-in
   * image and it should be visible what could be replaced. Package files are
   * listed too, read-only: they arrive and leave with the package.
   */
  function toggleAssets(card, themeName) {
    var open = card.querySelector(".theme-assets");
    if (open) {
      open.remove();
      return;
    }

    var panel = element("div", "theme-assets");
    card.appendChild(panel);

    fetch("/api/themes/" + encodeURIComponent(themeName) + "/assets", { credentials: "same-origin" })
      .then(function (response) {
        return response.json();
      })
      .then(function (payload) {
        (payload.assets || []).forEach(function (asset) {
          panel.appendChild(assetRow(themeName, asset, card));
        });
        (payload.files || []).forEach(function (file) {
          panel.appendChild(fileRow(file));
        });
      })
      .catch(function (error) {
        say(strings.failed + " " + error.message);
      });
  }

  function fileRow(file) {
    var row = element("div", "asset-row");

    var label = element("div", "asset-label");
    label.appendChild(element("span", "asset-slot", file.path));
    label.appendChild(element("span", "hint", strings.packageFile));
    row.appendChild(label);
    row.appendChild(element("span", "sample", file.content_type));
    return row;
  }

  function assetRow(themeName, asset, card) {
    var row = element("div", "asset-row");

    var label = element("div", "asset-label");
    label.appendChild(element("span", "asset-slot", asset.slot));
    label.appendChild(element("span", "hint", asset.description));
    row.appendChild(label);
    row.appendChild(element("span", "badge builtin", asset.custom ? strings.imageCustom : strings.imageDefault));

    var actions = element("div", "theme-actions");

    var upload = element("label", "button small secondary", strings.imageReplace);
    var input = document.createElement("input");
    input.type = "file";
    input.accept = "image/svg+xml,image/png,image/webp,image/jpeg,image/gif";
    input.hidden = true;
    input.addEventListener("change", function (event) {
      var file = event.target.files[0];
      if (file) {
        putAsset(themeName, asset.slot, file, card);
      }
      event.target.value = "";
    });
    upload.appendChild(input);
    actions.appendChild(upload);

    if (asset.custom) {
      var reset = element("button", "button small danger", strings.imageReset);
      reset.addEventListener("click", function () {
        fetch("/api/themes/" + encodeURIComponent(themeName) + "/assets/" + encodeURIComponent(asset.slot), {
          method: "DELETE",
          credentials: "same-origin"
        })
          .then(function (response) {
            if (!response.ok) {
              throw new Error(failed(response, null));
            }
            reopenAssets(card, themeName);
          })
          .catch(function (error) {
            say(error.message);
          });
      });
      actions.appendChild(reset);
    }

    row.appendChild(actions);
    return row;
  }

  function putAsset(themeName, slot, file, card) {
    say(strings.uploading + " " + file.name);

    var body = new FormData();
    body.append("file", file);

    fetch("/api/themes/" + encodeURIComponent(themeName) + "/assets/" + encodeURIComponent(slot), {
      method: "PUT",
      credentials: "same-origin",
      body: body
    })
      .then(function (response) {
        return response.json().then(function (payload) {
          if (!response.ok) {
            throw new Error(failed(response, payload));
          }
          say(strings.uploaded + " " + slot);
          reopenAssets(card, themeName);
        });
      })
      .catch(function (error) {
        say(error.message);
      });
  }

  function reopenAssets(card, themeName) {
    toggleAssets(card, themeName); // close
    toggleAssets(card, themeName); // and reload
  }

  function load() {
    return fetch("/api/themes", { credentials: "same-origin" })
      .then(function (response) {
        return response.json();
      })
      .then(function (payload) {
        list.textContent = "";
        var themes = payload.themes || [];
        if (!themes.length) {
          list.appendChild(element("p", "empty", strings.empty));
          return;
        }
        themes.forEach(function (item) {
          list.appendChild(row(item));
        });
      })
      .catch(function (error) {
        say(strings.failed + " " + error.message);
      });
  }

  document.getElementById("import").addEventListener("change", function (event) {
    var file = event.target.files[0];
    if (!file) {
      return;
    }
    say(strings.uploading + " " + file.name);

    var body = new FormData();
    body.append("file", file);

    fetch("/api/themes/import?activate=1", {
      method: "POST",
      credentials: "same-origin",
      body: body
    })
      .then(function (response) {
        return response.json().then(function (payload) {
          if (!response.ok) {
            throw new Error(failed(response, payload));
          }
          reloadOverlay();
          say(strings.uploaded + " " + payload.name);
          return load();
        });
      })
      .catch(function (error) {
        say(error.message);
      })
      .finally(function () {
        event.target.value = "";
      });
  });

  load();
})();
