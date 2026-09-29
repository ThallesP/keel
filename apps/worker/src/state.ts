import { log } from "./log";

// Resume points, persisted to a per-node volume so a restart does not replay or skip:
// `eventsSince` for the Docker event stream, `logsSince[containerId]` for each container tail.
// Losing the file costs one full observe sweep and at most `tail: 0` (new lines only) for logs.

const PATH = process.env.KEEL_STATE ?? "/var/lib/keel-worker/state.json";

export type State = {
  eventsSince?: string;
  logsSince: Record<string, string>;
};

let state: State = { logsSince: {} };
let dirty = false;

export async function loadState(): Promise<State> {
  try {
    const parsed = (await Bun.file(PATH).json()) as Partial<State>;
    state = { eventsSince: parsed.eventsSince, logsSince: parsed.logsSince ?? {} };
  } catch {
    state = { logsSince: {} };
  }
  return state;
}

export const getState = () => state;

export function touch() {
  dirty = true;
}

/** Written at most once a second; every caller marks `dirty` instead of writing itself. */
export function startStateWriter() {
  setInterval(() => {
    if (!dirty) return;
    dirty = false;
    void Bun.write(PATH, JSON.stringify(state)).catch((err) =>
      log("state", `write failed: ${String(err)}`),
    );
  }, 1000);
}
