import { cn } from "@my-better-t-app/ui/lib/utils";

export function Segmented<T extends string>({
  options,
  value,
  onChange,
  label,
  mono = false,
}: {
  options: { id: T; label: string; title?: string }[];
  value: T;
  onChange: (value: T) => void;
  label: string;
  mono?: boolean;
}) {
  return (
    <span
      className="flex h-7 items-center rounded-md border border-line p-0.5"
      role="group"
      aria-label={label}
    >
      {options.map((o) => (
        <button
          key={o.id}
          type="button"
          aria-pressed={o.id === value}
          title={o.title}
          onClick={() => onChange(o.id)}
          className={cn(
            "h-full rounded-sm px-2 text-2xs",
            mono && "font-mono",
            o.id === value ? "bg-surface-2 text-ink" : "text-muted-foreground hover:text-ink",
          )}
        >
          {o.label}
        </button>
      ))}
    </span>
  );
}
