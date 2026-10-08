import { toast } from "sonner";

import { errorMessage } from "@/lib/api";

/** The sentence to show for a failure (an ApiError's problem `detail`). */
export { errorMessage };

/**
 * What `attempt` resolves to. Writes that answer 204 resolve `undefined` on success, so success
 * is `ok`, never "the value is defined" (web-data.md §9.4).
 */
export type Attempt<T> = { ok: true; data: T } | { ok: false };

/** Surface a failed mutation/action as a toast. Resolves `{ ok: false }` on failure. */
export async function attempt<T>(promise: Promise<T>): Promise<Attempt<T>> {
  try {
    return { ok: true, data: await promise };
  } catch (err) {
    toast.error(errorMessage(err));
    return { ok: false };
  }
}
