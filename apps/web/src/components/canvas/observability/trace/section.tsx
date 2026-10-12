import type { ReactNode } from "react";

import { SectionLabel } from "../../primitives";

export function Section({
  title,
  empty,
  children,
}: {
  title: string;
  empty?: string;
  children: ReactNode;
}) {
  return (
    <section className="flex flex-col gap-2">
      <SectionLabel>{title}</SectionLabel>
      {children || <p className="text-2xs text-faint">{empty}</p>}
    </section>
  );
}
