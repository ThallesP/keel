import { cn } from "@my-better-t-app/ui/lib/utils";

/** Glyph + wordmark. The topbar's; the auth pages show it above the form. */
export function Logo({ className }: { className?: string }) {
  return (
    <span className={cn("flex items-center gap-2", className)}>
      <svg width="22" height="22" viewBox="0 0 22 22" aria-hidden>
        <path d="M3 6.5h16l-2.2 6.5H6.5L3 6.5Z" fill="var(--color-primary)" />
        <path
          d="M6.5 13v3.5h9V13"
          fill="none"
          stroke="var(--color-primary)"
          strokeWidth="1.8"
          strokeLinecap="round"
        />
      </svg>
      <span className="text-[15px] leading-[18px] font-semibold tracking-tight text-ink">keel</span>
    </span>
  );
}
