// Web Push: asking this browser to be told things, and the service worker that
// receives them.
//
// # What this can and cannot promise
//
// **On an iPhone, nothing at all unless the site has been added to the Home
// Screen.** Safari delivers Web Push only to an installed web app, so an
// iPhone user who has not installed this register gets no push however many
// times they press the button. That is why the page says so rather than
// leaving somebody to wonder, and why the in-app notification list has to
// stand on its own: everything is written there first and delivered second.
(function () {
  "use strict";

  function show(element, message) {
    if (!element) return;
    element.textContent = message;
    element.hidden = false;
  }

  // installed reports whether the page is running as an installed web app,
  // which is the only way an iPhone receives a push at all.
  function installed() {
    return (
      window.matchMedia("(display-mode: standalone)").matches ||
      // Safari's own, non-standard, and the one that actually answers on iOS.
      window.navigator.standalone === true
    );
  }

  function isApple() {
    const ua = navigator.userAgent || "";
    return /iPhone|iPad|iPod/i.test(ua);
  }

  document.addEventListener("DOMContentLoaded", function () {
    const root = document.querySelector("[data-push]");
    if (!root) return;

    const button = root.querySelector("[data-push-subscribe]");
    const status = root.querySelector("[data-push-status]");
    const key = root.dataset.pushKey || "";

    // An installation with no VAPID keys cannot send, and the page says so
    // instead of offering a permission prompt for something that would never
    // arrive.
    if (!key) {
      show(status, root.dataset.stringUnconfigured || "");
      if (button) button.disabled = true;
      return;
    }

    if (!("serviceWorker" in navigator) || !("PushManager" in window)) {
      show(status, root.dataset.stringUnsupported || "");
      if (button) button.disabled = true;
      return;
    }

    // The honest limitation, said before the button is pressed rather than
    // after nothing happens.
    if (isApple() && !installed()) {
      show(status, root.dataset.stringIosInstall || "");
    }

    if (Notification.permission === "denied") {
      // A refused permission cannot be asked for again from script: the
      // browser only lets the person change it in their own settings, so the
      // page has to say that rather than offer a button that does nothing.
      show(status, root.dataset.stringDenied || "");
      if (button) button.disabled = true;
      return;
    }

    if (!button) return;

    button.addEventListener("click", async function () {
      button.disabled = true;
      try {
        const permission = await Notification.requestPermission();
        if (permission !== "granted") {
          show(status, root.dataset.stringDenied || "");
          return;
        }

        // Registered at the root so the worker controls the whole origin: a
        // worker served from /static would be scoped to /static, and a push
        // arriving for a group page would have nobody to receive it.
        const registration = await navigator.serviceWorker.register("/sw.js", {
          scope: "/",
        });
        await navigator.serviceWorker.ready;

        const subscription = await registration.pushManager.subscribe({
          // Required by every browser: a push this site cannot read the
          // contents of would be a push the browser refuses to deliver
          // silently, and that is worse than not having one.
          userVisibleOnly: true,
          applicationServerKey: window.doleances.fromBase64URL(key),
        });

        const json = subscription.toJSON();
        await window.doleances.call("/api/notifications/subscribe", {
          endpoint: json.endpoint,
          p256dh: json.keys.p256dh,
          auth: json.keys.auth,
          label: window.doleances.deviceLabel(),
        });

        window.location.reload();
      } catch (cause) {
        show(status, (cause && cause.message) || root.dataset.stringFailed || "");
      } finally {
        button.disabled = false;
      }
    });
  });
})();
