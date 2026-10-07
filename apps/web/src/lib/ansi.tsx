import { cn } from "@my-better-t-app/ui/lib/utils";
import { useEffect, useSyncExternalStore } from "react";

import AnsiWorker from "./ansi.worker?worker";

// Log lines keep the color codes their process wrote (`ESC[32m INFO ESC[0m`). Parsing them is
// done in a worker, like t3code highlights off the main thread: the stream re-polls every few
// seconds and a long tail must not stall the canvas. Parsed lines are cached by their text, so a
// poll only ships lines it has not seen, and each line re-renders only when its own entry lands.
// Until the worker answers, a line shows with its codes stripped.

export type AnsiSegment = {
  text: string;
  fg?: string;
  bg?: string;
  bold?: true;
  dim?: true;
  italic?: true;
  underline?: true;
};
export type AnsiRequest = { texts: string[] };
export type AnsiResponse = { texts: string[]; parsed: AnsiSegment[][] };

const ESC = "\u001b";
/** CSI sequences: SGR colors plus the cursor/erase codes progress bars write. */
const CSI = new RegExp(String.raw`${ESC}\[[0-9;?]*[ -/]*[@-~]`, "g");

/** The line as plain text, for matching and titles. */
export const stripAnsi = (text: string) => (text.includes(ESC) ? text.replace(CSI, "") : text);

const CACHE_MAX = 5000;
const cache = new Map<string, AnsiSegment[]>();
const pending = new Set<string>();
const listeners = new Set<() => void>();
/** Lines asked for in this tick, shipped to the worker as one message. */
let queued: string[] = [];
/** null once it failed to start: lines then stay stripped. */
let worker: Worker | null | undefined;

function getWorker() {
  if (worker !== undefined) return worker;
  try {
    worker = new AnsiWorker();
    worker.onmessage = ({ data }: MessageEvent<AnsiResponse>) => {
      data.texts.forEach((text, i) => {
        pending.delete(text);
        cache.set(text, data.parsed[i] ?? []);
      });
      for (const key of cache.keys()) {
        if (cache.size <= CACHE_MAX) break;
        cache.delete(key);
      }
      for (const l of listeners) l();
    };
    worker.onerror = () => {
      worker?.terminate();
      worker = null;
    };
  } catch {
    worker = null;
  }
  return worker;
}

function request(text: string) {
  if (cache.has(text) || pending.has(text)) return;
  pending.add(text);
  if (queued.push(text) > 1) return;
  queueMicrotask(() => {
    const texts = queued;
    queued = [];
    getWorker()?.postMessage({ texts } satisfies AnsiRequest);
  });
}

const subscribe = (l: () => void) => {
  listeners.add(l);
  return () => listeners.delete(l);
};

/** One line in its ANSI colors, mapped onto the canvas palette. Plain lines render as they are. */
export function AnsiText({ text }: { text: string }) {
  const colored = text.includes(ESC);
  const segments = useSyncExternalStore(subscribe, () => (colored ? cache.get(text) : undefined));
  useEffect(() => {
    if (colored) request(text);
  }, [colored, text]);
  if (!segments) return colored ? stripAnsi(text) : text;
  return segments.map((s, i) => (
    <span
      // Segments of one immutable line: the index is their identity.
      key={i}
      className={cn(
        s.bold && "font-semibold",
        s.dim && "opacity-60",
        s.italic && "italic",
        s.underline && "underline",
      )}
      style={s.fg || s.bg ? { color: s.fg, backgroundColor: s.bg } : undefined}
    >
      {s.text}
    </span>
  ));
}
