import { cn } from "@my-better-t-app/ui/lib/utils";

import { deckHalfBreadth, depth, halfBreadth, heightAtHalfBreadth, sheer } from "./hull";

/**
 * A lines plan: the drawing a shipyard works from, and the first line on it is the keel. Three
 * views cut from the hull in `hull.ts`: the sheer plan (side), the half-breadth plan (from above,
 * one side only, as drawn) and the body plan (sections, bow half right, stern half left). The keel
 * is the one line in colour.
 *
 * `submerged` raises the water over the hull in the two views that have a waterline; the auth
 * shell does that while a password is being typed.
 */

const W = 600;
const H = 430;

// Sheer plan: hull from X0 to X1, deck at its lowest point at DECK_Y, keel DEPTH below it.
const X0 = 40;
const X1 = 560;
const DECK_Y = 92;
const SHEER = 90;
const DEPTH = 64;
const SHEER_TOP = 68;
const SHEER_BOTTOM = 166;

// Half-breadth plan: breadths drawn up from the centreline.
const PLAN_CL = 248;
const BREADTH = 44;

// Body plan: same hull, enlarged.
const BODY_CX = 300;
const BODY_SCALE = 1.8;
const BODY_DECK_Y = 290;
const BODY_TOP = 262;
const BODY_BOTTOM = 412;

const STATIONS = 10;
/** Height above the keel, as a fraction of the depth. The third one is the design waterline. */
const WATERLINES = [0.2, 0.4, 0.6, 0.8];
const DWL = 2;
const BUTTOCKS = [0.33, 0.66];

const px = (x: number) => X0 + x * (X1 - X0);
const deckY = (x: number) => DECK_Y - sheer(x) * SHEER;
const keelY = (x: number) => deckY(x) + depth(x) * DEPTH;
const bodyDeckY = (x: number) => BODY_DECK_Y - sheer(x) * SHEER * BODY_SCALE;
const bodyKeelY = (x: number) => bodyDeckY(x) + depth(x) * DEPTH * BODY_SCALE;
const KEEL_MID_Y = DECK_Y + DEPTH;
const BODY_KEEL_MID_Y = BODY_DECK_Y + DEPTH * BODY_SCALE;
const waterlineY = (f: number) => KEEL_MID_Y - f * DEPTH;
const bodyWaterlineY = (f: number) => BODY_KEEL_MID_Y - f * DEPTH * BODY_SCALE;

type Pt = [number, number];
const samples = (n: number) => Array.from({ length: n + 1 }, (_, i) => i / n);

function d(points: Pt[]): string {
  return points.map(([x, y], i) => `${i ? "L" : "M"}${x.toFixed(1)} ${y.toFixed(1)}`).join(" ");
}

/** Splits a sampled curve where it is undefined, so a buttock that ends at the deck ends there. */
function dSegments(points: (Pt | null)[]): string {
  const runs: Pt[][] = [];
  let run: Pt[] = [];
  for (const p of points) {
    if (p) run.push(p);
    else if (run.length) {
      runs.push(run);
      run = [];
    }
  }
  if (run.length) runs.push(run);
  return runs.map(d).join(" ");
}

const xs = samples(120);

const deckLine = d(xs.map((x) => [px(x), deckY(x)]));
const keelLine = d(xs.map((x) => [px(x), keelY(x)]));

const buttockProfiles = BUTTOCKS.map((b) =>
  dSegments(
    xs.map((x) => {
      const t = heightAtHalfBreadth(x, b);
      return t === null ? null : [px(x), keelY(x) - t * (keelY(x) - deckY(x))];
    }),
  ),
);

const deckPlan = d(xs.map((x) => [px(x), PLAN_CL - deckHalfBreadth(x) * BREADTH]));

const waterlinePlans = WATERLINES.map((f) =>
  dSegments(
    xs.map((x) => {
      const t = (keelY(x) - waterlineY(f)) / (keelY(x) - deckY(x));
      return t <= 0 ? null : [px(x), PLAN_CL - halfBreadth(x, Math.min(t, 1)) * BREADTH];
    }),
  ),
);

const ts = samples(24);
const bodySections = Array.from({ length: STATIONS + 1 }, (_, j) => {
  const x = j / STATIONS;
  const sides = j === STATIONS / 2 ? [-1, 1] : x > 0.5 ? [1] : [-1];
  return sides.map((side) =>
    d(
      ts.map((t) => [
        BODY_CX + side * halfBreadth(x, t) * BREADTH * BODY_SCALE,
        bodyKeelY(x) - t * (bodyKeelY(x) - bodyDeckY(x)),
      ]),
    ),
  );
}).flat();

const bodyDeckLine = [1, -1].map((side) =>
  d(
    xs
      .filter((x) => (side > 0 ? x >= 0.5 : x <= 0.5))
      .map((x) => [BODY_CX + side * deckHalfBreadth(x) * BREADTH * BODY_SCALE, bodyDeckY(x)]),
  ),
);

const label = "fill-faint font-mono text-[9.5px] tracking-[0.08em]";
const thin = { fill: "none", strokeWidth: 0.8, vectorEffect: "non-scaling-stroke" } as const;

function Water({
  clip,
  x1,
  x2,
  restY,
  topY,
  submerged,
}: {
  clip: string;
  x1: number;
  x2: number;
  restY: number;
  topY: number;
  submerged: boolean;
}) {
  // At rest a faint hatch below the waterline. Submerged, it rises and turns opaque enough to
  // hide the hull, keel included.
  return (
    <g clipPath={`url(#${clip})`}>
      <g
        className="motion-safe:transition-transform motion-safe:duration-700 motion-safe:ease-[cubic-bezier(.2,.7,.2,1)]"
        style={{ transform: `translateY(${submerged ? topY - restY : 0}px)` }}
      >
        <rect
          x={x1}
          y={restY}
          width={x2 - x1}
          height={H}
          className={cn(
            "fill-primary-soft motion-safe:transition-opacity motion-safe:duration-700",
            submerged ? "opacity-90" : "opacity-0",
          )}
        />
        <rect x={x1} y={restY} width={x2 - x1} height={H} fill="url(#water-hatch)" />
        <line x1={x1} x2={x2} y1={restY} y2={restY} className="stroke-primary" {...thin} />
      </g>
    </g>
  );
}

export function LinesPlan({ submerged, className }: { submerged: boolean; className?: string }) {
  return (
    <svg
      viewBox={`0 0 ${W} ${H}`}
      className={cn("h-auto w-full select-none", className)}
      role="img"
      aria-label="Lines plan of a hull: sheer plan, half-breadth plan and body plan, with the keel drawn in blue"
    >
      <defs>
        <pattern
          id="water-hatch"
          width={7}
          height={7}
          patternUnits="userSpaceOnUse"
          patternTransform="rotate(-18)"
        >
          <line x1={0} x2={7} y1={3.5} y2={3.5} className="stroke-primary/20" strokeWidth={0.8} />
        </pattern>
        <clipPath id="sheer-clip">
          <rect x={0} y={SHEER_TOP} width={W} height={SHEER_BOTTOM - SHEER_TOP} />
        </clipPath>
        <clipPath id="body-clip">
          <rect x={0} y={BODY_TOP} width={W} height={BODY_BOTTOM - BODY_TOP} />
        </clipPath>
      </defs>

      {/* Station grid through the sheer and half-breadth plans. */}
      {Array.from({ length: STATIONS + 1 }, (_, j) => (
        <g key={j}>
          <line
            x1={px(j / STATIONS)}
            x2={px(j / STATIONS)}
            y1={SHEER_TOP}
            y2={PLAN_CL}
            className="stroke-dot"
            {...thin}
          />
          <text x={px(j / STATIONS)} y={178} textAnchor="middle" className={label}>
            {j}
          </text>
        </g>
      ))}

      {/* Sheer plan */}
      <text x={X0} y={58} className={label}>
        SHEER PLAN
      </text>
      {WATERLINES.map((f, i) => (
        <g key={f}>
          <line
            x1={X0 - 8}
            x2={X1 + 8}
            y1={waterlineY(f)}
            y2={waterlineY(f)}
            className={i === DWL ? "stroke-muted-foreground" : "stroke-dot"}
            {...thin}
          />
          <text x={X1 + 12} y={waterlineY(f) + 3} className={label}>
            {i === DWL ? "DWL" : `WL${i + 1}`}
          </text>
        </g>
      ))}
      {buttockProfiles.map((p) => (
        <path key={p} d={p} className="stroke-faint" {...thin} />
      ))}
      <path d={deckLine} className="stroke-ink" {...thin} />
      <line x1={px(0)} x2={px(0)} y1={deckY(0)} y2={keelY(0)} className="stroke-ink" {...thin} />
      <line x1={px(1)} x2={px(1)} y1={deckY(1)} y2={keelY(1)} className="stroke-ink" {...thin} />
      <path d={keelLine} className="stroke-primary" fill="none" strokeWidth={1.6} />
      <Water
        clip="sheer-clip"
        x1={X0 - 8}
        x2={X1 + 8}
        restY={waterlineY(WATERLINES[DWL])}
        topY={SHEER_TOP}
        submerged={submerged}
      />
      <text
        x={X1 + 8}
        y={SHEER_TOP + 10}
        textAnchor="end"
        className={cn(
          label,
          "fill-primary motion-safe:transition-opacity motion-safe:duration-500",
          submerged ? "opacity-100" : "opacity-0",
        )}
      >
        BELOW THE WATERLINE
      </text>

      {/* Half-breadth plan */}
      <text x={X0} y={194} className={label}>
        HALF-BREADTH PLAN
      </text>
      <line
        x1={X0 - 8}
        x2={X1 + 8}
        y1={PLAN_CL}
        y2={PLAN_CL}
        className="stroke-muted-foreground"
        {...thin}
      />
      <text x={X1 + 12} y={PLAN_CL + 3} className={label}>
        CL
      </text>
      {BUTTOCKS.map((b) => (
        <line
          key={b}
          x1={X0 - 8}
          x2={X1 + 8}
          y1={PLAN_CL - b * BREADTH}
          y2={PLAN_CL - b * BREADTH}
          className="stroke-dot"
          strokeDasharray="3 3"
          {...thin}
        />
      ))}
      {waterlinePlans.map((p) => (
        <path key={p} d={p} className="stroke-faint" {...thin} />
      ))}
      <path d={deckPlan} className="stroke-ink" {...thin} />
      <line
        x1={px(0)}
        x2={px(0)}
        y1={PLAN_CL - deckHalfBreadth(0) * BREADTH}
        y2={PLAN_CL}
        className="stroke-ink"
        {...thin}
      />

      {/* Body plan */}
      <text x={BODY_CX} y={BODY_TOP - 6} textAnchor="middle" className={label}>
        BODY PLAN
      </text>
      <line
        x1={BODY_CX}
        x2={BODY_CX}
        y1={BODY_TOP}
        y2={BODY_BOTTOM}
        className="stroke-muted-foreground"
        {...thin}
      />
      {BUTTOCKS.map((b) =>
        [-1, 1].map((side) => (
          <line
            key={`${b}${side}`}
            x1={BODY_CX + side * b * BREADTH * BODY_SCALE}
            x2={BODY_CX + side * b * BREADTH * BODY_SCALE}
            y1={BODY_TOP}
            y2={BODY_BOTTOM}
            className="stroke-dot"
            strokeDasharray="3 3"
            {...thin}
          />
        )),
      )}
      {WATERLINES.map((f, i) => (
        <line
          key={f}
          x1={BODY_CX - BREADTH * BODY_SCALE - 12}
          x2={BODY_CX + BREADTH * BODY_SCALE + 12}
          y1={bodyWaterlineY(f)}
          y2={bodyWaterlineY(f)}
          className={i === DWL ? "stroke-muted-foreground" : "stroke-dot"}
          {...thin}
        />
      ))}
      {bodySections.map((p) => (
        <path key={p} d={p} className="stroke-faint" {...thin} />
      ))}
      {bodyDeckLine.map((p) => (
        <path key={p} d={p} className="stroke-ink" {...thin} />
      ))}
      <line
        x1={BODY_CX - 14}
        x2={BODY_CX + 14}
        y1={BODY_KEEL_MID_Y}
        y2={BODY_KEEL_MID_Y}
        className="stroke-primary"
        strokeWidth={1.6}
      />
      <Water
        clip="body-clip"
        x1={BODY_CX - BREADTH * BODY_SCALE - 12}
        x2={BODY_CX + BREADTH * BODY_SCALE + 12}
        restY={bodyWaterlineY(WATERLINES[DWL])}
        topY={BODY_TOP}
        submerged={submerged}
      />
      <text
        x={BODY_CX - BREADTH * BODY_SCALE - 16}
        y={BODY_BOTTOM}
        textAnchor="end"
        className={label}
      >
        AFT
      </text>
      <text x={BODY_CX + BREADTH * BODY_SCALE + 16} y={BODY_BOTTOM} className={label}>
        FORE
      </text>
    </svg>
  );
}
