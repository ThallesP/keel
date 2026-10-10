// The generated fetch client's runtime configuration, and the error type every API call throws.
//
// The generated functions (`listNodes`, `signIn`, …) and hooks (`useListNodes`, …) all go through
// one client (src/api/gen/.kubb/client.ts). setupApiClient, called once in main.tsx, makes it:
//   - same-origin with credentials (the HttpOnly `keel_session` cookie; no tokens in JS),
//   - throw ApiError for any non-2xx answer (RFC 9457 problem: status, code, detail),
//   - read-your-writes: a write's `Keel-Invalidate` topics are refetched before it resolves,
//   - drop the session when an answer says the caller is no longer signed in (401).
import type { QueryClient } from "@tanstack/react-query";

import { type DeviceError, type Problem, client } from "@/api/gen";

import { cachedSession, invalidateTopics, markSignedOut, parseTopics } from "./query";

/** Response header naming the realtime topics a write changed (web-data.md §9.3). */
export const INVALIDATE_HEADER = "Keel-Invalidate";

/**
 * A non-2xx API answer. `message` is the sentence to show (the problem's `detail`, which the
 * server keeps identical to the Convex error texts), `code` the stable code (`NOT_FOUND`,
 * `NAME_TAKEN`, … see docs/go/ARCHITECTURE.md "Errors"). The device-login endpoints answer the
 * RFC 8628 shape instead of a problem; then `code` is its `error` (`authorization_pending`, …)
 * and `message` its `error_description`.
 */
export class ApiError extends Error {
  override readonly name = "ApiError";
  /** HTTP status. */
  readonly status: number;
  /** Problem `code`, or the RFC 8628 `error`; `UNKNOWN` when the body had neither. */
  readonly code: string;
  /** Problem `detail` / RFC 8628 `error_description`, when present. */
  readonly detail: string | undefined;
  /** Problem `title` (the status text), when present. */
  readonly title: string | undefined;
  /** The parsed error body (Problem, DeviceError, or whatever the server sent). */
  readonly body: unknown;
  readonly method: string | undefined;
  /** Request URL without query string. */
  readonly url: string | undefined;

  constructor(init: {
    status: number;
    statusText?: string;
    body?: unknown;
    method?: string;
    url?: string;
  }) {
    const { body } = init;
    const problem = isObject(body) ? (body as Partial<Problem> & Partial<DeviceError>) : undefined;
    const detail = str(problem?.detail) ?? str(problem?.error_description);
    const title = str(problem?.title) ?? (init.statusText?.trim() || undefined);
    super(
      detail ??
        title ??
        (typeof body === "string" && body.trim() ? body.trim() : `HTTP ${init.status}`),
    );
    this.status = init.status;
    this.code = str(problem?.code) ?? str(problem?.error) ?? "UNKNOWN";
    this.detail = detail;
    this.title = title;
    this.body = body;
    this.method = init.method;
    this.url = init.url?.split(/[?#]/)[0];
  }

  /** The RFC 9457 problem, when the body is one. */
  get problem(): Problem | undefined {
    return isObject(this.body) && typeof (this.body as Problem).code === "string"
      ? (this.body as Problem)
      : undefined;
  }
}

/** Whether `err` is an ApiError, optionally with one of the given codes. */
export function isApiError(err: unknown, ...codes: string[]): err is ApiError {
  return err instanceof ApiError && (codes.length === 0 || codes.includes(err.code));
}

/**
 * The sentence to show for a failure: an ApiError's message (the server's sentence), else the
 * first line of an Error's message, else the value as text. Use it for every toast and inline
 * error.
 */
export function errorMessage(err: unknown): string {
  if (err instanceof ApiError) return err.message;
  if (err instanceof Error) return err.message.split("\n")[0] ?? err.message;
  return String(err);
}

let installed: { client: typeof client; response: number; error: number } | undefined;

/**
 * Configures the generated client for this QueryClient. Call once at start (main.tsx), before
 * anything renders; calling it again replaces the previous setup.
 */
export function setupApiClient(queryClient: QueryClient): void {
  if (installed) {
    installed.client.interceptors.response.eject(installed.response);
    installed.client.interceptors.error.eject(installed.error);
  }
  // Relative URLs (the API is on the dashboard's origin) and the session cookie.
  client.setConfig({ baseURL: "", credentials: "same-origin" });

  // Read-your-writes: refetch what the write changed before its promise resolves, so a component
  // never renders its own write missing. The socket's message for the same topics follows and
  // refetches once more (harmless).
  const response = client.interceptors.response.use(async (result, config) => {
    const method = (config?.method ?? "GET").toUpperCase();
    if (method !== "GET" && method !== "HEAD") {
      const topics = parseTopics(result.headers.get(INVALIDATE_HEADER));
      if (topics.length > 0) await invalidateTopics(queryClient, topics);
    }
    return result;
  });

  // Every non-2xx becomes an ApiError. A 401 while the cache believes in a session means the
  // session is gone (expired, signed out elsewhere): flip to signed out, like Convex dropping auth.
  const error = client.interceptors.error.use((err) => {
    const apiError = new ApiError({
      status: err.status,
      statusText: err.statusText,
      body: err.data,
      method: err.request instanceof Request ? err.request.method : undefined,
      url: err.request instanceof Request ? err.request.url : undefined,
    });
    if (apiError.status === 401 && cachedSession(queryClient)?.user) {
      void markSignedOut(queryClient);
    }
    throw apiError;
  });

  installed = { client, response, error };
}

function isObject(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null;
}

function str(v: unknown): string | undefined {
  return typeof v === "string" && v.trim() !== "" ? v : undefined;
}
