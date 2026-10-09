import { cn } from "@my-better-t-app/ui/lib/utils";

export function LegendKey({
  swatch,
  color,
  label,
}: {
  swatch: "rect" | "line";
  color: string;
  label: string;
}) {
  return (
    <span className="flex items-center gap-1.5 text-2xs text-muted-foreground">
      <span
        className={cn(
          "shrink-0",
          swatch === "rect" ? "size-2 rounded-[2px]" : "h-0.5 w-3 rounded-full",
        )}
        style={{ background: color }}
      />
      {label}
    </span>
  );
}
