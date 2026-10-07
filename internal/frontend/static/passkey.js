// Passkeys: the one part of signing in that cannot be server-rendered.
//
// `navigator.credentials` lives in the browser and there is no way around
// that — it is the API that talks to the fingerprint reader, the security key
// and the platform keychain. So this file exists, and it is deliberately the
// whole of what runs here: it converts between the wire format and the
// browser's ArrayBuffers, calls the browser, and posts the answer back.
//
// It holds no credential of its own and it never sees the session token. The
// frontend turns that into an HttpOnly cookie and strips it from the answer
// before this code can read it, because the same token authorises adding and
// removing passkeys — and a script that could read it could send it somewhere.
(function () {
  "use strict";

  // --- the wire format ------------------------------------------------------
  //
  // WebAuthn speaks ArrayBuffers; JSON does not. Everything on the wire is
  // base64url, unpadded, which is what the Go side produces and expects.

  function fromBase64URL(value) {
    const padded = value.replace(/-/g, "+").replace(/_/g, "/");
    const raw = atob(padded + "=".repeat((4 - (padded.length % 4)) % 4));
    const bytes = new Uint8Array(raw.length);
    for (let i = 0; i < raw.length; i++) bytes[i] = raw.charCodeAt(i);
    return bytes;
  }

  function toBase64URL(buffer) {
    const bytes = new Uint8Array(buffer);
    let raw = "";
    for (let i = 0; i < bytes.length; i++) raw += String.fromCharCode(bytes[i]);
    return btoa(raw).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
  }

  // decodeCreation turns the server's registration options into what
  // navigator.credentials.create wants.
  function decodeCreation(options) {
    const decoded = Object.assign({}, options);
    decoded.challenge = fromBase64URL(options.challenge);
    decoded.user = Object.assign({}, options.user, {
      id: fromBase64URL(options.user.id),
    });
    if (options.excludeCredentials) {
      decoded.excludeCredentials = options.excludeCredentials.map(function (c) {
        return Object.assign({}, c, { id: fromBase64URL(c.id) });
      });
    }
    return decoded;
  }

  // decodeRequest does the same for a sign-in.
  function decodeRequest(options) {
    const decoded = Object.assign({}, options);
    decoded.challenge = fromBase64URL(options.challenge);
    if (options.allowCredentials) {
      decoded.allowCredentials = options.allowCredentials.map(function (c) {
        return Object.assign({}, c, { id: fromBase64URL(c.id) });
      });
    }
    return decoded;
  }

  function encodeAttestation(credential) {
    return {
      id: credential.id,
      rawId: toBase64URL(credential.rawId),
      type: credential.type,
      // Whether the authenticator itself verified the person — a fingerprint,
      // a face, a PIN — as opposed to merely being present.
      authenticatorAttachment: credential.authenticatorAttachment || undefined,
      clientExtensionResults: credential.getClientExtensionResults(),
      response: {
        clientDataJSON: toBase64URL(credential.response.clientDataJSON),
        attestationObject: toBase64URL(credential.response.attestationObject),
        transports: credential.response.getTransports
          ? credential.response.getTransports()
          : undefined,
      },
    };
  }

  function encodeAssertion(credential) {
    return {
      id: credential.id,
      rawId: toBase64URL(credential.rawId),
      type: credential.type,
      authenticatorAttachment: credential.authenticatorAttachment || undefined,
      clientExtensionResults: credential.getClientExtensionResults(),
      response: {
        clientDataJSON: toBase64URL(credential.response.clientDataJSON),
        authenticatorData: toBase64URL(credential.response.authenticatorData),
        signature: toBase64URL(credential.response.signature),
        // The handle is what makes a usernameless sign-in work: the
        // authenticator stores it beside the key and hands it back with
        // whichever credential the person chose, so nobody types anything.
        userHandle: credential.response.userHandle
          ? toBase64URL(credential.response.userHandle)
          : null,
      },
    };
  }

  // --- what this device says it is -----------------------------------------
  //
  // A label for the person's own device list, so it does not read "a passkey"
  // six times over — a list like that is one nobody can revoke from safely,
  // and revoking the wrong row is how somebody locks themselves out of the
  // device they are holding.
  //
  // It is a claim, never evidence. The server cuts it and sanitises it like
  // any other text that arrives from outside.
  function deviceLabel() {
    const data = navigator.userAgentData;
    const platform = (data && data.platform) || guessPlatform();
    const browser = guessBrowser();
    if (platform && browser) return browser + " on " + platform;
    return browser || platform || "a device";
  }

  function guessPlatform() {
    const ua = navigator.userAgent || "";
    if (/Android/i.test(ua)) return "Android";
    if (/iPhone|iPad|iPod/i.test(ua)) return "iOS";
    if (/Mac OS X/i.test(ua)) return "macOS";
    if (/Windows/i.test(ua)) return "Windows";
    if (/Linux/i.test(ua)) return "Linux";
    return "";
  }

  function guessBrowser() {
    const ua = navigator.userAgent || "";
    // Order matters: every one of these carries the others' names.
    if (/Edg\//.test(ua)) return "Edge";
    if (/OPR\//.test(ua)) return "Opera";
    if (/Firefox\//.test(ua)) return "Firefox";
    if (/Chrome\//.test(ua)) return "Chrome";
    if (/Safari\//.test(ua)) return "Safari";
    return "";
  }

  // --- talking to this site -------------------------------------------------

  async function call(path, body) {
    const response = await fetch(path, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      // Same origin, and the session cookie has to travel: it is what says
      // which account a new passkey belongs to.
      credentials: "same-origin",
      body: JSON.stringify(body || {}),
    });

    const answer = await response.json().catch(function () {
      return {};
    });
    if (!response.ok) {
      const problem = new Error(answer.detail || answer.title || "refused");
      problem.status = response.status;
      throw problem;
    }
    return answer;
  }

  async function send(path, method, body) {
    const response = await fetch(path, {
      method: method,
      headers: { "Content-Type": "application/json" },
      credentials: "same-origin",
      body: JSON.stringify(body || {}),
    });
    const answer = await response.json().catch(function () {
      return {};
    });
    if (!response.ok) {
      const problem = new Error(answer.detail || answer.title || "refused");
      problem.status = response.status;
      throw problem;
    }
    return answer;
  }

  // --- the ceremonies -------------------------------------------------------

  // register makes an account, or adds a passkey to the one already signed in.
  //
  // The two are the same exchange against different endpoints, which is why
  // they share this function: a difference between them would be a difference
  // in how a credential is produced, and there is none.
  //
  // **The two requests carry different things, and that is not incidental.**
  // This took one argument for both at first, merged into each, and it was
  // wrong in a way no Go test could see: a signup sends the chosen name to
  // `begin` and the backend refused the whole registration because `finish`
  // does not accept one.
  //
  // It does not accept one on purpose. The name travels with the server-held
  // challenge exactly so that the client cannot choose it at the end — the
  // same reason the user handle does — so `finish` carrying a name would be a
  // field worth attacking. Device linking is the opposite case: its token is
  // needed by both halves, because the second is what spends it.
  async function register(paths, beginWith, finishWith) {
    const begun = await call(paths.begin, beginWith || {});
    const credential = await navigator.credentials.create({
      publicKey: decodeCreation(begun.options),
    });
    if (!credential) throw new Error("cancelled");

    return call(
      paths.finish,
      Object.assign(
        {
          ceremony: begun.ceremony,
          credential: encodeAttestation(credential),
          device_label: deviceLabel(),
        },
        finishWith || {}
      )
    );
  }

  // signIn asks the browser for any passkey it holds for this register.
  //
  // Nothing is asked for first, because there is nothing to ask for: the
  // account has no username, the browser offers what it has, and the answer
  // says which account it was.
  async function signIn() {
    const begun = await call("/api/account/login/begin", {});
    const credential = await navigator.credentials.get({
      publicKey: decodeRequest(begun.options),
      // Not conditional mediation here: this is a button somebody pressed, so
      // the modal prompt is the expected one. Autofill-style sign-in is
      // handled separately below, where there is a field to attach it to.
      mediation: "optional",
    });
    if (!credential) throw new Error("cancelled");

    return call("/api/account/login/finish", {
      ceremony: begun.ceremony,
      credential: encodeAssertion(credential),
      device_label: deviceLabel(),
    });
  }

  // --- wiring ---------------------------------------------------------------

  function supported() {
    return !!(
      window.PublicKeyCredential &&
      navigator.credentials &&
      navigator.credentials.create
    );
  }

  function show(element, message) {
    if (!element) return;
    element.textContent = message;
    element.hidden = false;
  }

  function busy(button, on) {
    if (!button) return;
    button.disabled = on;
    button.setAttribute("aria-busy", on ? "true" : "false");
  }

  // refused turns what went wrong into something worth reading.
  //
  // Only two cases are worth telling apart, and neither is a server error. A
  // cancelled prompt is not a failure — somebody changed their mind — and a
  // duplicate passkey means the authenticator already holds one for this
  // account, which is the `excludeCredentials` list doing its job.
  function refused(element, cause, strings) {
    if (cause && (cause.name === "NotAllowedError" || cause.message === "cancelled")) {
      show(element, strings.cancelled);
      return;
    }
    if (cause && cause.name === "InvalidStateError") {
      show(element, strings.already);
      return;
    }
    show(element, (cause && cause.message) || strings.failed);
  }

  document.addEventListener("DOMContentLoaded", function () {
    const root = document.querySelector("[data-passkey]");
    if (!root) return;

    const strings = {
      unsupported: root.dataset.stringUnsupported || "",
      cancelled: root.dataset.stringCancelled || "",
      already: root.dataset.stringAlready || "",
      failed: root.dataset.stringFailed || "",
    };
    const error = root.querySelector("[data-passkey-error]");
    const next = root.dataset.next || "/account";

    if (!supported()) {
      // Said plainly rather than left as a button that does nothing. A browser
      // without WebAuthn cannot take part, and pretending otherwise wastes
      // somebody's time on a prompt that will never appear.
      const warning = root.querySelector("[data-passkey-unsupported]");
      if (warning) warning.hidden = false;
      root.querySelectorAll("button[data-passkey-action]").forEach(function (button) {
        button.disabled = true;
      });
      return;
    }

    root.querySelectorAll("button[data-passkey-action]").forEach(function (button) {
      button.addEventListener("click", async function () {
        if (error) error.hidden = true;
        busy(button, true);
        try {
          switch (button.dataset.passkeyAction) {
            case "signup":
              await register(
                {
                  begin: "/api/account/register/begin",
                  finish: "/api/account/register/finish",
                },
                { name: nameField() },
                {}
              );
              // The recovery codes come back in that answer and are shown
              // once, by the account page, which is where they belong: a sheet
              // of codes on a sign-in page is a sheet nobody writes down.
              window.location.assign("/account?fresh=1");
              return;

            case "signin":
              await signIn();
              window.location.assign(next);
              return;

            case "add":
              await register(
                {
                  begin: "/api/account/me/passkeys/begin",
                  finish: "/api/account/me/passkeys/finish",
                },
                {},
                {}
              );
              window.location.reload();
              return;
          }
        } catch (cause) {
          refused(error, cause, strings);
        } finally {
          busy(button, false);
        }
      });
    });

    function nameField() {
      const field = root.querySelector("[data-passkey-name]");
      return field ? field.value.trim() : "";
    }

    // --- signing in with a recovery code ---------------------------------
    //
    // For somebody whose every device is gone. It is not a passkey ceremony at
    // all, and it lives here because it is the other way through the same door.
    const recovery = root.querySelector("[data-recovery-form]");
    if (recovery) {
      recovery.addEventListener("submit", async function (event) {
        event.preventDefault();
        if (error) error.hidden = true;

        const field = recovery.querySelector("[data-recovery-code]");
        try {
          await call("/api/account/recover", {
            code: field ? field.value : "",
            device_label: deviceLabel(),
          });
          window.location.assign(next);
        } catch (cause) {
          refused(error, cause, strings);
        }
      });
    }

    // --- linking a new device --------------------------------------------
    //
    // The new device's half. It claims the code, waits for the old device to
    // approve what claimed it, and only then registers a passkey — which is
    // the step that stops a relayed code, because somebody who tricked a
    // person into scanning theirs still has to get that person to approve a
    // browser they do not recognise.
    const linking = root.querySelector("[data-link-token]");
    if (linking) {
      const token = linking.dataset.linkToken;
      const status = root.querySelector("[data-link-status]");

      (async function () {
        try {
          await call("/api/account/link/claim", {
            token: token,
            label: deviceLabel(),
          });
          show(status, root.dataset.stringWaiting || "");

          // Polled rather than pushed. A socket for a two-minute window would
          // be machinery for one screen, and the window is short enough that
          // a few seconds of latency costs nobody anything.
          const deadline = Date.now() + 2 * 60 * 1000;
          while (Date.now() < deadline) {
            const state = await call("/api/account/link/status", { token: token });
            if (state.confirmed) {
              show(status, root.dataset.stringApproved || "");
              await register(
                {
                  begin: "/api/account/link/register/begin",
                  finish: "/api/account/link/register/finish",
                },
                { token: token },
                { token: token }
              );
              window.location.assign("/account");
              return;
            }
            await new Promise(function (resolve) {
              setTimeout(resolve, 2000);
            });
          }
          show(error, root.dataset.stringExpired || strings.failed);
        } catch (cause) {
          refused(error, cause, strings);
        }
      })();
    }
  });

  // Exposed for the account page's own script, which subscribes this browser
  // to notifications and needs the same device label and the same caller.
  window.doleances = window.doleances || {};
  window.doleances.deviceLabel = deviceLabel;
  window.doleances.call = call;
  window.doleances.send = send;
  window.doleances.fromBase64URL = fromBase64URL;
})();
