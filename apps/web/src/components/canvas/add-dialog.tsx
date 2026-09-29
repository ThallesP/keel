import { Container, GitBranch } from "lucide-react";
import { useMemo } from "react";

import { Palette, type PalettePage } from "@/components/palette";

import type { CreateOptions } from "./actions";
import { NodeTypeIcon } from "./nodes/icons";
import { notWired } from "./not-wired";
import type { InfraNodeType } from "./types";

/** What a new node runs; `type` decides the shell, the rest the container. */
export type AddChoice = { type: InfraNodeType } & CreateOptions;

const ENGINES: {
  engine: NonNullable<CreateOptions["engine"]>;
  type: InfraNodeType;
  label: string;
  hint: string;
}[] = [
  { engine: "postgres", type: "database", label: "PostgreSQL", hint: "postgres:16 · DATABASE_URL" },
  { engine: "mysql", type: "database", label: "MySQL", hint: "mysql:8 · DATABASE_URL" },
  { engine: "mongo", type: "database", label: "MongoDB", hint: "mongo:7 · DATABASE_URL" },
  { engine: "redis", type: "cache", label: "Redis", hint: "redis:7 · REDIS_URL" },
];

function pages(pick: (choice: AddChoice) => void): PalettePage {
  const service: PalettePage = {
    title: "Service",
    placeholder: "Type an image, e.g. ghcr.io/acme/api:latest",
    onSubmit: (image) => pick({ type: "service", image, deploy: true }),
    submitLabel: (image) => `Deploy ${image}`,
    submitHint: "Docker image · ships right away",
    submitIcon: <Container size={14} strokeWidth={1.5} aria-hidden />,
    items: [
      {
        id: "github",
        label: "GitHub repo",
        hint: "Build from source · coming soon",
        icon: <GitBranch size={14} strokeWidth={1.5} aria-hidden />,
        disabled: true,
        onSelect: () => notWired("GitHub repo"),
      },
      {
        id: "nginx",
        label: "nginx",
        hint: "nginx:alpine · placeholder service",
        keywords: ["docker", "image"],
        icon: <Container size={14} strokeWidth={1.5} aria-hidden />,
        onSelect: () => pick({ type: "service", deploy: true }),
      },
    ],
  };

  const database: PalettePage = {
    title: "Database",
    placeholder: "Which engine?",
    items: ENGINES.map((e) => ({
      id: e.engine,
      label: e.label,
      hint: e.hint,
      keywords: [e.type, e.engine],
      icon: <NodeTypeIcon type={e.type} />,
      onSelect: () => pick({ type: e.type, engine: e.engine, deploy: true }),
    })),
  };

  return {
    title: "Add",
    placeholder: "What do you want to run?",
    items: [
      {
        id: "service",
        label: "Service",
        hint: "Docker image or GitHub repo",
        icon: <NodeTypeIcon type="service" />,
        onSelect: () => service,
      },
      {
        id: "database",
        label: "Database",
        hint: "Postgres, MySQL, MongoDB, Redis",
        keywords: ENGINES.map((e) => e.label.toLowerCase()),
        icon: <NodeTypeIcon type="database" />,
        onSelect: () => database,
      },
      {
        id: "volume",
        label: "Volume",
        hint: "Persistent disk · 10 GB",
        icon: <NodeTypeIcon type="volume" />,
        onSelect: () => pick({ type: "volume" }),
      },
    ],
  };
}

/**
 * The `+ Add` / `N` palette. Root lists Service / Database / Volume; Service takes an image as
 * free text (type → ↵), Database lists engines. Picking a leaf calls `onPick` and closes.
 * Every deployable pick ships immediately: adding is deploying.
 */
export function AddDialog({
  open,
  onOpenChange,
  onPick,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onPick: (choice: AddChoice) => void;
}) {
  const root = useMemo(() => pages(onPick), [onPick]);
  return <Palette open={open} onOpenChange={onOpenChange} root={root} />;
}
