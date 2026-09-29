import { ConvexError } from "convex/values";
import { toast } from "sonner";

export function errorMessage(err: unknown): string {
  if (err instanceof ConvexError) return String(err.data);
  if (err instanceof Error) return err.message.split("\n")[0] ?? err.message;
  return String(err);
}

/** Surface a failed mutation/action as a toast. Returns undefined on failure. */
export async function attempt<T>(promise: Promise<T>): Promise<T | undefined> {
  try {
    return await promise;
  } catch (err) {
    toast.error(errorMessage(err));
    return undefined;
  }
}
