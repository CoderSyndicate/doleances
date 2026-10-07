// The service worker. It does one thing: show a notification.
//
// It is deliberately not a cache, not an offline shell and not a router. A
// worker that served pages would be a second copy of this site's routing to
// keep in step with the first, and the register has no offline story it has
// promised anybody.

self.addEventListener("install", function () {
  // Active immediately rather than waiting for every tab to close. There is no
  // cached state for an old worker to be consistent with, so the usual reason
  // to wait does not apply.
  self.skipWaiting();
});

self.addEventListener("activate", function (event) {
  event.waitUntil(self.clients.claim());
});

self.addEventListener("push", function (event) {
  let payload = {};
  try {
    payload = event.data ? event.data.json() : {};
  } catch (e) {
    payload = {};
  }

  const title = payload.title || "doléances";
  event.waitUntil(
    self.registration.showNotification(title, {
      body: payload.body || "",
      // The tag is what keeps "one notification, not a campaign" honest across
      // devices: every subscribed device buzzing once is correct — the rule is
      // one per thing, not one per screen — but the same event arriving twice
      // on one device is not, and this collapses it.
      tag: payload.tag || undefined,
      data: { url: payload.url || "/notifications" },
      icon: "/theme/assets/mark",
      badge: "/theme/assets/mark",
      // Not silent, and not requiring interaction either: a meeting next week
      // is worth a buzz and is not an emergency.
      requireInteraction: false,
    })
  );
});

self.addEventListener("notificationclick", function (event) {
  event.notification.close();

  const target = (event.notification.data && event.notification.data.url) || "/notifications";
  event.waitUntil(
    self.clients.matchAll({ type: "window", includeUncontrolled: true }).then(function (windows) {
      // An open tab is focused rather than a second one opened: somebody who
      // already has the register open does not want two of it.
      for (const client of windows) {
        if ("focus" in client) {
          client.navigate(target);
          return client.focus();
        }
      }
      return self.clients.openWindow(target);
    })
  );
});
