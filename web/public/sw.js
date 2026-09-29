// Lanscape service worker: offline shell and a cache of the last API state.
const SHELL = "lanscape-shell-v1";
const DATA = "lanscape-data-v1";

self.addEventListener("install", (e) => {
  e.waitUntil(caches.open(SHELL).then((c) => c.addAll(["/", "/manifest.webmanifest", "/icon.svg"])));
  self.skipWaiting();
});

self.addEventListener("activate", (e) => {
  e.waitUntil(
    caches.keys().then((keys) => Promise.all(keys.filter((k) => k !== SHELL && k !== DATA).map((k) => caches.delete(k)))),
  );
  self.clients.claim();
});

self.addEventListener("fetch", (e) => {
  const url = new URL(e.request.url);
  if (e.request.method !== "GET" || url.origin !== location.origin || url.pathname === "/api/v1/events") return;
  if (url.pathname.startsWith("/api/")) {
    // network first, fall back to the last successful response (offline view)
    e.respondWith(
      fetch(e.request)
        .then((r) => {
          if (r.ok) {
            const copy = r.clone();
            caches.open(DATA).then((c) => c.put(e.request, copy));
          }
          return r;
        })
        .catch(() => caches.match(e.request).then((r) => r || Response.error())),
    );
    return;
  }
  e.respondWith(
    fetch(e.request)
      .then((r) => {
        if (r.ok && (url.pathname.startsWith("/assets/") || url.pathname === "/")) {
          const copy = r.clone();
          caches.open(SHELL).then((c) => c.put(e.request, copy));
        }
        return r;
      })
      .catch(() => caches.match(e.request).then((r) => r || caches.match("/"))),
  );
});

self.addEventListener("push", (e) => {
  const d = e.data ? e.data.json() : { title: "Lanscape", body: "" };
  e.waitUntil(
    self.registration.showNotification(d.title || "Lanscape", {
      body: d.body || "",
      icon: "/icon.svg",
      tag: d.tag,
      renotify: !!d.tag,
      requireInteraction: d.severity === "down",
      data: d,
    }),
  );
});

self.addEventListener("notificationclick", (e) => {
  e.notification.close();
  const url = (e.notification.data && e.notification.data.url) || "/";
  e.waitUntil(
    self.clients.matchAll({ type: "window", includeUncontrolled: true }).then((list) => {
      const open = list.find((c) => new URL(c.url).origin === location.origin);
      if (open) return open.focus().then((c) => (url !== "/" && c ? c.navigate(url) : c));
      return self.clients.openWindow(url);
    }),
  );
});
