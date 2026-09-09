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
 * # E9b: the shell IS cached now, and why that reverses the note above
 *
 * The original version of this file refused to cache `index.html`, on the
 * grounds that booting the app with no data produces a broken-looking client
 * rather than an honest offline page. That reasoning was right for E9a and is
 * wrong now, for one reason: there IS data. `src/offline/` gives the app an
 * IndexedDB cache of mailboxes, headers and read bodies, so a shell served from
 * cache boots into real mail rather than into an empty frame.
 *
 * So navigation is still NETWORK-FIRST — a working network always wins, and a
 * deploy still takes effect on the next load — but the fallback ladder now has
 * three rungs instead of one:
 *
 *   1. the network;
 *   2. the cached `index.html`, which boots the app against IndexedDB;
 *   3. `offline.html`, if the shell was never cached (a first visit that went
 *      offline before the worker finished installing).
 *
 * The shell is re-cached on every successful navigation, which is what keeps it
 * in step with the deployed build: the hashed asset URLs inside it change on
 * each deploy, so a stale shell would reference chunks that 404. Storing the
 * copy the network just served means the shell and the assets it names are
 * always the same generation.
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
// v2 (E9b): the app shell joins the cache, so the version is bumped to retire
// every v1 cache and start the shell entry clean.
const CACHE_VERSION = "v2";
const CACHE_PREFIX = "moov-";
const CACHE_NAME = `${CACHE_PREFIX}${CACHE_VERSION}`;

/** The offline page, precached at install so it is there when it is needed. */
const OFFLINE_URL = "/offline.html";

/**
 * The app shell's cache key.
 *
 * A FIXED key rather than the request URL, because every route in this app —
 * `/mail/inbox`, `/search`, `/label/work` — is served the same `index.html` by
 * Caddy. Keying by URL would store one copy per route the user happened to
 * visit online, and would miss on any route they had not.
 */
const SHELL_URL = "/index.html";

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
      /*
       * E9b: precache the shell too, so the very first offline visit after
       * installation boots the app rather than the fallback page. It is a
       * SEPARATE `catch` because a failure here must not fail the install —
       * losing the shell costs offline boot, while losing the whole worker
       * costs offline detection as well.
       */
      try {
        const shell = await fetch(new Request(SHELL_URL, { cache: "reload" }));
        if (shell.ok) await cache.put(SHELL_URL, shell);
      } catch {
        // Offline during install. The next successful navigation stores it.
      }
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

  /*
   * Everything else falls through to the network untouched. It is small, it is
   * already HTTP-cached, and adding it here would buy nothing but a second
   * staleness surface.
   *
   * Note what is NOT in that "everything else" any more: the shell links its
   * manifest, favicon and apple-touch-icon under `/branding/...`, so they were
   * already returned above by `isBypassed` — deliberately. Those responses are
   * resolved per Host by the server, and a worker that cached them would serve
   * one customer's icon on another customer's host. The prefix match covers
   * every current and future path under it, so nothing has to be added here
   * when the server grows another branded asset.
   */
});

/**
 * Navigations: network-first, then the cached shell, then the offline page.
 *
 * The network still WINS whenever it works, which is what keeps a deploy taking
 * effect on the next load instead of after an eviction. The difference from
 * E9a is what happens when it does not: the app shell boots against the
 * IndexedDB cache (see `src/offline/`) instead of a dead-end page.
 *
 * A successful navigation refreshes the stored shell, so the copy on disk
 * always names the same hashed assets as the running build. A stale shell would
 * reference chunks that no longer exist, which fails as a blank page — worse
 * than being offline.
 */
async function handleNavigation(request) {
  const cache = await caches.open(CACHE_NAME);
  try {
    // `cache: "no-cache"` forces revalidation against the origin instead of
    // letting the browser's HTTP cache answer. Without it, a shell served
    // without Cache-Control is considered heuristically fresh (10% of its
    // age since Last-Modified), so a deploy could keep serving the OLD
    // index.html — naming OLD hashed assets, which this worker then serves
    // cache-first — for hours. Seen live on 2026-09-09: the network had the
    // new build, the tab kept running the previous one. The origin also
    // sends Cache-Control: no-cache now (deploy/Caddyfile.public); this is
    // the belt to that suspender, so a misconfigured front cannot reopen it.
    const response = await fetch(request, { cache: "no-cache" });
    if (response.ok) {
      // Store a CLONE: the original is consumed by the browser rendering it.
      cache.put(SHELL_URL, response.clone()).catch(() => {
        // A full quota must not fail the navigation the user is waiting on.
      });
    }
    return response;
  } catch {
    const shell = await cache.match(SHELL_URL);
    if (shell !== undefined) return shell;

    const offline = await cache.match(OFFLINE_URL);
    if (offline !== undefined) return offline;
    // Both are missing (a failed install that then went offline). Say so
    // plainly rather than letting the browser show its own error page.
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
