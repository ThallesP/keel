import { useNow } from "@/components/canvas/use-now";
import { Logo } from "@/components/logo";

import { shipsBells } from "./bells";
import { RealArtistsShip } from "./plate";

/**
 * The page around sign-in, sign-up, invites and CLI approval. Left, on the canvas ground: what a
 * keel is (the first thing laid when a ship is built; the first account lays this one), and the
 * plate (`plate.tsx`). Right: the form.
 */
export function AuthShell({ children }: { children: React.ReactNode }) {
  return (
    <div className="flex h-svh bg-bg">
      <Plate />
      <main className="flex min-w-0 flex-1 flex-col items-center justify-center px-6 py-10">
        <div className="w-full max-w-sm">
          <Logo className="mb-10" />
          {children}
        </div>
      </main>
    </div>
  );
}

function Plate() {
  const now = useNow(30_000);
  const time = new Date(now);
  const clock = `${String(time.getHours()).padStart(2, "0")}:${String(time.getMinutes()).padStart(2, "0")}`;
  return (
    <aside
      aria-hidden
      className="hidden w-1/2 max-w-3xl flex-col justify-between border-r border-line bg-canvas p-10 lg:flex"
      style={{
        backgroundImage: "radial-gradient(var(--color-dot) 1px, transparent 1px)",
        backgroundSize: "20px 20px",
      }}
    >
      <dl className="max-w-sm">
        <dt className="text-md font-semibold tracking-tight text-ink">
          keel <span className="ml-1 font-mono text-sm font-normal text-faint">/kiːl/ · n.</span>
        </dt>
        <dd className="mt-2 text-sm leading-relaxed text-muted-foreground">
          The first timber laid when a ship is built. Everything else is fastened to it.
        </dd>
      </dl>

      <RealArtistsShip />

      <dl className="grid max-w-md grid-cols-[auto_1fr] gap-x-6 gap-y-1 text-2xs text-faint">
        <dt>Source</dt>
        <dd className="text-muted-foreground">folklore.org, Andy Hertzfeld</dd>
        <dt>Host</dt>
        <dd className="truncate font-mono text-muted-foreground">{window.location.host}</dd>
        <dt>Time</dt>
        <dd className="text-muted-foreground">
          {clock} · {shipsBells(time)}
        </dd>
      </dl>
    </aside>
  );
}
