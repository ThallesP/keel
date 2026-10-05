/**
 * A small hull, described the way a naval architect would for a lines plan: a deck outline, a
 * profile (sheer and keel) and a section shape per station. Everything the drawing shows
 * (waterlines, buttocks, stations) is cut from this one surface.
 *
 * Coordinates are normalised: `x` runs 0 (stern) → 1 (bow), `t` runs 0 (keel) → 1 (deck).
 * Breadths are a fraction of the maximum half-breadth, depths a fraction of the maximum depth.
 */

const MIDSHIP = 0.42;

/** Half-breadth at the deck: full midships, fine at the stem, a transom aft. */
export function deckHalfBreadth(x: number): number {
  if (x >= MIDSHIP) {
    const u = (x - MIDSHIP) / (1 - MIDSHIP);
    return Math.pow(Math.max(0, 1 - Math.pow(u, 2.2)), 0.65);
  }
  const v = (MIDSHIP - x) / MIDSHIP;
  return 1 - 0.45 * Math.pow(v, 2.5);
}

/** Sheer: how far the deck rises above its lowest point, as a fraction of the depth. */
export function sheer(x: number): number {
  const d = x - 0.45;
  return d * d * (d > 0 ? 0.36 : 0.18);
}

/** Depth of the keel below the deck. Flat along the middle, the forefoot rises to the stem. */
export function depth(x: number): number {
  if (x > 0.78) {
    const w = (x - 0.78) / 0.22;
    return 1 - 0.88 * w * w;
  }
  if (x < 0.1) {
    const w = (0.1 - x) / 0.1;
    return 1 - 0.5 * w * w;
  }
  return 1;
}

/** Section fullness: a round bilge midships (superellipse exponent ~2.6), nearly a V at the ends. */
function fullness(x: number): number {
  return 1.15 + 1.5 * Math.pow(Math.max(0, 1 - Math.abs(x - 0.45) / 0.55), 1.2);
}

/** Half-breadth of the section at station `x`, height `t` above the keel. */
export function halfBreadth(x: number, t: number): number {
  if (t <= 0) return 0;
  if (t >= 1) return deckHalfBreadth(x);
  const k = fullness(x);
  return deckHalfBreadth(x) * Math.pow(1 - Math.pow(1 - t, k), 1 / k);
}

/** Inverse of `halfBreadth` in `t`: the height where the section reaches half-breadth `b`. */
export function heightAtHalfBreadth(x: number, b: number): number | null {
  const bd = deckHalfBreadth(x);
  if (b >= bd) return null;
  const k = fullness(x);
  return 1 - Math.pow(1 - Math.pow(b / bd, k), 1 / k);
}
