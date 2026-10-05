import { Logo } from "@/components/logo";

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
  return (
    <aside
      className="hidden w-1/2 max-w-3xl flex-col justify-center gap-20 border-r border-line bg-canvas p-10 lg:flex"
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
    </aside>
  );
}
