/// <reference lib="webworker" />
import Anser from "anser";

import type { AnsiRequest, AnsiResponse, AnsiSegment } from "./ansi";

/**
 * Terminal colors re-tuned for the light canvas: the 16 standard ones arrive as anser's
 * default RGB and land on Keel tones dark enough to read at 11px. 256/truecolor pass through.
 */
const PALETTE: Record<string, string> = {
  "0, 0, 0": "var(--color-ink)",
  "187, 0, 0": "#c62f2f",
  "0, 187, 0": "#178a48",
  "187, 187, 0": "#9a6c00",
  "0, 0, 187": "#2f55c9",
  "187, 0, 187": "#9b3fa8",
  "0, 187, 187": "#1b7f8f",
  "255,255,255": "var(--color-ink)",
  "85, 85, 85": "var(--color-faint)",
  "255, 85, 85": "#c62f2f",
  "0, 255, 0": "#178a48",
  "255, 255, 85": "#9a6c00",
  "85, 85, 255": "#2f55c9",
  "255, 85, 255": "#9b3fa8",
  "85, 255, 255": "#1b7f8f",
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
