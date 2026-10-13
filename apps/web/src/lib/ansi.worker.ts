/// <reference lib="webworker" />
import Anser from "anser";

import type { AnsiRequest, AnsiResponse, AnsiSegment } from "./ansi";

/**
 * The 16 standard terminal colors land on `--ansi-*` (globals.css), tuned to read at 11px in
 * either theme. 256/truecolor pass through.
 */
const PALETTE: Record<string, string> = {
  "0, 0, 0": "var(--color-ink)",
  "187, 0, 0": "var(--ansi-red)",
  "0, 187, 0": "var(--ansi-green)",
  "187, 187, 0": "var(--ansi-yellow)",
  "0, 0, 187": "var(--ansi-blue)",
  "187, 0, 187": "var(--ansi-magenta)",
  "0, 187, 187": "var(--ansi-cyan)",
  "255,255,255": "var(--color-ink)",
  "85, 85, 85": "var(--color-faint)",
  "255, 85, 85": "var(--ansi-red)",
  "0, 255, 0": "var(--ansi-green)",
  "255, 255, 85": "var(--ansi-yellow)",
  "85, 85, 255": "var(--ansi-blue)",
  "255, 85, 255": "var(--ansi-magenta)",
  "85, 255, 255": "var(--ansi-cyan)",
  "255, 255, 255": "var(--color-ink)",
};

const color = (rgb: string | null) => (rgb ? (PALETTE[rgb] ?? `rgb(${rgb})`) : undefined);

function parse(text: string): AnsiSegment[] {
  return Anser.ansiToJson(text, { remove_empty: true }).map((e) => {
    const bg = color(e.bg);
    const d = e.decorations;
    return {
      text: e.content,
      fg: color(e.fg),
      bg: bg && `color-mix(in srgb, ${bg} 14%, transparent)`,
      bold: d.includes("bold") || undefined,
      dim: d.includes("dim") || undefined,
      italic: d.includes("italic") || undefined,
      underline: d.includes("underline") || undefined,
    };
  });
}

self.onmessage = (e: MessageEvent<AnsiRequest>) => {
  const res: AnsiResponse = { texts: e.data.texts, parsed: e.data.texts.map(parse) };
  self.postMessage(res);
};
