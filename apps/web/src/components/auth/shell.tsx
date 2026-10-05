import { useEffect, useState } from "react";

import { useNow } from "@/components/canvas/use-now";
import { Logo } from "@/components/logo";

import { shipsBells } from "./bells";
import { LinesPlan } from "./lines-plan";

/**
 * The page around sign-in, sign-up, invites and CLI approval. Left, on the canvas ground: the
 * lines plan of a hull, because the keel is the first thing laid when a ship is built and the
 * first account lays this one. While a password is being typed the hull goes below the
 * waterline (the drawing's version of a mascot covering its eyes). Right: the form.
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

/** True while the focused element is a password field. */
function usePasswordFocus(): boolean {
  const [focused, setFocused] = useState(false);
  useEffect(() => {
    const isPassword = (el: EventTarget | null) =>
      el instanceof HTMLInputElement && el.type === "password";
    const onIn = (e: FocusEvent) => setFocused(isPassword(e.target));
    const onOut = () => setFocused(false);
    document.addEventListener("focusin", onIn);
    document.addEventListener("focusout", onOut);
    return () => {
      document.removeEventListener("focusin", onIn);
      document.removeEventListener("focusout", onOut);
    };
  }, []);
  return focused;
}

function Plate() {
  const submerged = usePasswordFocus();
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

      <LinesPlan submerged={submerged} className="my-8 max-h-[52vh]" />

      <div className="grid max-w-md grid-cols-[auto_1fr] gap-x-6 gap-y-1 font-mono text-2xs text-faint uppercase tracking-[0.08em]">
        <span>Drawing</span>
        <span className="text-muted-foreground">Lines plan, sheet 1 of 1</span>
        <span>Yard</span>
        <span className="truncate text-muted-foreground normal-case">{window.location.host}</span>
        <span>Time</span>
        <span className="text-muted-foreground">
          {clock} · {shipsBells(time)}
        </span>
      </div>
    </aside>
  );
}
