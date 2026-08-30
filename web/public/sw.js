/*
 * Moov's service worker (E9, decision D-1: Moov is an installable PWA).
 *
 * # The governing rule: this worker must never be able to show stale mail
 *
 * A service worker sits between the app and the network for EVERY request on
 * the origin, including the ones that carry a user's mail. That makes a
 * caching mistake here qualitatively worse than a caching mistake anywhere
 * else in the stack: a stale asset is a cosmetic bug, but a stale JMAP
 * response is a user reading a message they already deleted, or missing one
 * that arrived, with no way to tell that is what happened.
 *
 * So the strategy is deliberately conservative and the DENY LIST comes first:
 *
 *   - Anything dynamic (JMAP, SSE, branding, blobs, uploads) is not merely
 *     "network-first" — this worker does not touch it at all. It falls through
 *     to the network as if no worker were installed.
 *   - Navigations are network-first with an offline FALLBACK page. The app
 *     shell is never served from cache while the network works, so a deploy
 *     takes effect on the next load rather than after some eviction.
 *   - Only Vite's content-hashed `/assets/*` are cache-first, which is safe
 *     precisely because their names change when their content does.
 *
 * There is no offline mode here. That is a later epic, and pretending
 * otherwise — precaching the shell and letting it boot into an app with no
 * data — would produce a broken-looking client rather than an honest
 * "you're offline" page.
 */

/*
 * The cache name carries a version. Bumping it is what retires every previous
 * cache in `activate`, which is the only mechanism that can recover a user
 * whose cached asset is somehow wrong.
 *
 * It is a literal rather than a build-time injection on purpose: this file is
 * served verbatim from `public/`, so a placeholder would need a build step to
 * rewrite it, and a build step that silently failed would ship the
 * placeholder. A constant a human edits cannot half-work.
 */
const CACHE_VERSION = "v1";
const CACHE_PREFIX = "moov-";
const CACHE_NAME = `${CACHE_PREFIX}${CACHE_VERSION}`;

/** The offline page, precached at install so it is there when it is needed. */
const OFFLINE_URL = "/offline.html";

/*
 * Paths this worker must never see, let alone cache.
 *
 * These are the exact route vocabularies the Caddyfile sends to moovd, plus
 * the two the app builds itself (blob hrefs and the SSE stream). They are
 * matched by PREFIX on the pathname, so `/jmap/eventsource` is covered by
 * `/jmap` and a future sub-route is covered by construction.
 */
const BYPASS_PREFIXES = [
  "/jmap",
  "/.well-known/jmap",
  "/session",
  "/raw/",
  "/upload/",
  "/eventsource",
  "/branding",
];

function isBypassed(url) {
  return BYPASS_PREFIXES.some(
    (prefix) => url.pathname === prefix || url.pathname.startsWith(prefix),
  );
}

/** Vite's content-hashed output: immutable by construction, safe to cache. */
function isHashedAsset(url) {
  return url.pathname.startsWith("/assets/");
}

self.addEventListener("install", (event) => {
  event.waitUntil(
    (async () => {
      const cache = await caches.open(CACHE_NAME);
      // `reload` bypasses the HTTP cache, so a worker installing right after a
      // deploy cannot precache the PREVIOUS offline page.
      await cache.add(new Request(OFFLINE_URL, { cache: "reload" }));
      // Take over as soon as this worker is ready rather than waiting for
      // every tab to close. Safe here because the worker holds no state a
      // previous version could disagree with.
      await self.skipWaiting();
    })(),
  );
});

self.addEventListener("activate", (event) => {
  event.waitUntil(
    (async () => {
      // Retire every cache this app made under a different version. Scoped to
      // our own prefix so a co-hosted app's caches are never touched.
      const names = await caches.keys();
      await Promise.all(
        names
          .filter((name) => name.startsWith(CACHE_PREFIX) && name !== CACHE_NAME)
          .map((name) => caches.delete(name)),
      );
      await self.clients.claim();
    })(),
  );
});

self.addEventListener("fetch", (event) => {
  const { request } = event;

  // Only GET is ever considered. A cached POST is meaningless, and a JMAP
  // call is a POST — this is the second line of defence behind BYPASS_PREFIXES.
  if (request.method !== "GET") return;

  const url = new URL(request.url);

  // Cross-origin requests are none of this worker's business: it cannot read
  // most of their responses anyway, and caching opaque ones wastes quota.
  if (url.origin !== self.location.origin) return;

  if (isBypassed(url)) return;

  if (request.mode === "navigate") {
    event.respondWith(handleNavigation(request));
    return;
  }

  if (isHashedAsset(url)) {
    event.respondWith(handleAsset(request));
  }

  // Everything else — the manifest, icons, favicon — falls through to the
  // network untouched. They are small, they are already HTTP-cached, and
  // adding them here would buy nothing but a second staleness surface.
});

/**
 * Navigations: the network, always, with the offline page as the only
 * fallback.
 *
 * Note what is NOT here: a cached copy of `index.html`. Serving a stale shell
 * would boot the app against a JMAP server it may no longer match, which is a
 * far more confusing failure than an honest offline page.
 */
async function handleNavigation(request) {
  try {
    return await fetch(request);
  } catch {
    const cache = await caches.open(CACHE_NAME);
    const offline = await cache.match(OFFLINE_URL);
    if (offline !== undefined) return offline;
    // The offline page itself is missing (a failed install). Say so plainly
    // rather than letting the browser show its own error page.
    return new Response("Offline", {
      status: 503,
      headers: { "Content-Type": "text/plain; charset=utf-8" },
    });
  }
}

/**
 * Hashed assets: cache-first, because the hash in the filename IS the cache
 * key. A given URL's bytes can never change, so there is nothing to
 * revalidate.
 */
async function handleAsset(request) {
  const cache = await caches.open(CACHE_NAME);
  const hit = await cache.match(request);
  if (hit !== undefined) return hit;

  const response = await fetch(request);
  // Only a real 200 is stored. Caching an opaque or error response would
  // pin a failure until the next version bump.
  if (response.ok && response.status === 200) {
    cache.put(request, response.clone()).catch(() => {
      // A full quota must not fail the request the user is waiting on.
    });
  }
  return response;
}
