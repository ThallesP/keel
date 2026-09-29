import { toast } from "sonner";

/** For buttons on the mockup with no defined behaviour yet. */
export function notWired(label: string) {
  toast(`${label} is not wired yet`);
}
