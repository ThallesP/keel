// Docker's multiplexed log stream: 8-byte header [type, 0, 0, 0, len(u32 BE)] then `len` bytes
// of payload. Frames can split across HTTP chunks, so the parser keeps a carry buffer. With
// the json-file / local drivers every frame is one log entry, but nothing guarantees the entry
// ends in "\n" (partial writes), so lines are split again per stream after reassembly.

export type Frame = { stream: "stdout" | "stderr"; text: string };

export class FrameParser {
  private carry = new Uint8Array(0);
  private decoder = new TextDecoder();

  /** Returns whole frames decoded so far; keeps any trailing partial frame for the next call. */
  push(chunk: Uint8Array): Frame[] {
    let buf: Uint8Array;
    if (this.carry.length === 0) buf = chunk;
    else {
      buf = new Uint8Array(this.carry.length + chunk.length);
      buf.set(this.carry);
      buf.set(chunk, this.carry.length);
    }
    const out: Frame[] = [];
    let off = 0;
    while (off + 8 <= buf.length) {
      const type = buf[off]!;
      const len =
        ((buf[off + 4]! << 24) | (buf[off + 5]! << 16) | (buf[off + 6]! << 8) | buf[off + 7]!) >>>
        0;
      if (type > 2) {
        // Not multiplexed (TTY container): the whole stream is raw text.
        out.push({ stream: "stdout", text: this.decoder.decode(buf.subarray(off)) });
        this.carry = new Uint8Array(0);
        return out;
      }
      if (off + 8 + len > buf.length) break;
      out.push({
        stream: type === 2 ? "stderr" : "stdout",
        text: this.decoder.decode(buf.subarray(off + 8, off + 8 + len)),
      });
      off += 8 + len;
    }
    this.carry = buf.subarray(off).slice();
    return out;
  }
}

export type Line = { time: string; text: string; stream: "stdout" | "stderr" };

/** Splits frames into lines. `time` is the RFC3339Nano prefix Docker adds with timestamps=1. */
export class LineSplitter {
  private partial: Record<"stdout" | "stderr", string> = { stdout: "", stderr: "" };

  push(frame: Frame): Line[] {
    const text = this.partial[frame.stream] + frame.text;
    const parts = text.split("\n");
    this.partial[frame.stream] = parts.pop() ?? "";
    return parts.filter((p) => p.length > 0).map((raw) => parseLine(raw, frame.stream));
  }
}

function parseLine(raw: string, stream: "stdout" | "stderr"): Line {
  const line = raw.endsWith("\r") ? raw.slice(0, -1) : raw;
  const space = line.indexOf(" ");
  const stamp = space > 0 ? line.slice(0, space) : "";
  if (stamp.length >= 20 && stamp.endsWith("Z") && stamp[4] === "-") {
    return { time: stamp, text: line.slice(space + 1), stream };
  }
  return { time: "", text: line, stream };
}

/** RFC3339Nano → Docker `since` ("seconds.nanoseconds"), plus one nanosecond so it is exclusive. */
export function sinceAfter(stamp: string): string | undefined {
  const m = /^(.+?)(?:\.(\d{1,9}))?Z$/.exec(stamp);
  if (!m) return undefined;
  const secs = Math.floor(Date.parse(`${m[1]}Z`) / 1000);
  if (Number.isNaN(secs)) return undefined;
  let nanos = Number((m[2] ?? "").padEnd(9, "0")) + 1;
  let s = secs;
  if (nanos >= 1_000_000_000) {
    nanos -= 1_000_000_000;
    s += 1;
  }
  return `${s}.${String(nanos).padStart(9, "0")}`;
}
