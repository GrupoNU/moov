import { vi } from "vitest";

/**
 * Fetch stubs shared by the delegated sign-in tests.
 *
 * They live here rather than being re-spelled in each test file for one
 * reason that matters to the tests themselves: the interesting assertion in
 * almost every one of them is "the token is NOT in this URL", and that is
 * only trustworthy if every stub records the URL the same way. A
 * `String(input)` that quietly produced `[object Object]` for a `Request`
 * would make `expect(url).not.toContain(token)` pass for the wrong reason —
 * which is exactly the kind of green test that hides a leak.
 */

/** One recorded request. */
export interface RecordedRequest {
  readonly url: string;
  readonly init: RequestInit | undefined;
}

/** The URL of a fetch argument, in every form it can take. */
export function urlOf(input: RequestInfo | URL): string {
  if (input instanceof Request) return input.url;
  if (input instanceof URL) return input.toString();
  return input;
}

/** The Authorization header a recorded request carried, if any. */
export function authOf(request: RecordedRequest): string | undefined {
  return (request.init?.headers as Record<string, string> | undefined)?.Authorization;
}

/** The body a recorded request carried, as a string. */
export function bodyOf(request: RecordedRequest): string {
  const body = request.init?.body;
  return typeof body === "string" ? body : "";
}

/** A JSON response, ready to be returned by a stub. */
export function jsonResponse(
  status: number,
  body: string,
  headers: Record<string, string> = {},
): Response {
  return new Response(body === "" ? null : body, {
    status,
    headers: { "Content-Type": "application/json; charset=utf-8", ...headers },
  });
}

/**
 * A fetch stub that answers every call with the same response and records
 * what it was asked.
 *
 * Not `async`: the lint rule against an async function with no `await` is
 * right, and `Promise.resolve` says exactly as much.
 */
export function recordingFetch(
  response: () => Response,
): { fetchImpl: typeof fetch; seen: RecordedRequest[] } {
  const seen: RecordedRequest[] = [];
  const fetchImpl: typeof fetch = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    seen.push({ url: urlOf(input), init });
    return Promise.resolve(response());
  });
  return { fetchImpl, seen };
}

/** A fetch stub that fails at the transport layer. */
export function failingFetch(message = "Failed to fetch"): typeof fetch {
  return vi.fn(() => Promise.reject(new TypeError(message)));
}
