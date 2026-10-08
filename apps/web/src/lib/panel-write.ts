// Writes fired from the node panel (variables, tracing, networking) and the Settings page.
import { toast } from "sonner";

import { errorMessage } from "./api";

/**
 * Awaits a write; a failure becomes a toast with the server's sentence (the problem's detail).
 * Resolves whether it succeeded. Most of these writes answer 204 and resolve `undefined`, so
 * success is "did not throw" (web-data.md §9.4). Like every generated mutation, it resolves after
 * the queries the write changed were refetched (Keel-Invalidate).
 */
export async function succeeded(write: Promise<unknown>): Promise<boolean> {
  try {
    await write;
    return true;
  } catch (err) {
    toast.error(errorMessage(err));
    return false;
  }
}
