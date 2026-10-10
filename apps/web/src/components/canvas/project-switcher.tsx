import { getRouteApi, useNavigate } from "@tanstack/react-router";
import { Box, ChevronDown, Plus } from "lucide-react";
import { useCallback, useMemo, useState } from "react";

import { useCreateProject, useListProjects } from "@/gen/api";
import { Palette, type PalettePage } from "@/components/palette";

import { useEnvironment } from "./environment";
import { attempt } from "./errors";
import { useHotkey } from "./use-hotkey";

const route = getRouteApi("/_auth/p/$projectId");

const ICON = { size: 14, strokeWidth: 1.5, "aria-hidden": true } as const;

/**
 * The topbar's project name, `P` anywhere: a palette of every project in the organization (type
 * to filter) and New project (type a name → ↵). Switching keeps the page you are on (canvas,
 * Observability or Settings); an open deployment or trace belongs to the old project and is
 * dropped.
 */
export function ProjectSwitcher() {
  const { projectId, projectName } = useEnvironment();
  const { view } = route.useSearch();
  const navigate = useNavigate();
  const { data: projectList } = useListProjects();
  const { mutateAsync: createProject } = useCreateProject();
  const [open, setOpen] = useState(false);
  useHotkey(
    { key: "p" },
    useCallback(() => setOpen(true), []),
  );

  // Memoized: the palette starts over whenever `root` changes.
  const root = useMemo<PalettePage>(() => {
    const go = (slug: string, search: { view?: "observability" | "settings" } = {}) =>
      void navigate({ to: "/p/$projectId", params: { projectId: slug }, search });
    const newProject: PalettePage = {
      title: "New project",
      placeholder: "Name it, e.g. my-app",
      items: [],
      onSubmit: (name) => {
        void attempt(createProject({ body: { name } })).then((r) => r.ok && go(r.data.slug));
      },
      submitLabel: (name) => `Create ${name}`,
      submitHint: "Empty canvas · production environment",
      submitIcon: <Plus {...ICON} />,
    };
    const sorted = [...(projectList?.projects ?? [])].sort((a, b) => a.name.localeCompare(b.name));
    return {
      title: "Projects",
      placeholder: projectList ? "Switch to…" : "Loading projects…",
      items: [
        ...sorted.map((p) => ({
          id: p.id,
          label: p.name,
          hint: p.id === projectId ? "current" : undefined,
          keywords: [p.slug],
          icon: <Box {...ICON} />,
          onSelect: () => {
            if (p.id !== projectId) go(p.slug, view ? { view } : {});
          },
        })),
        {
          id: "new",
          label: "New project",
          keywords: ["create", "add"],
          icon: <Plus {...ICON} />,
          onSelect: () => newProject,
        },
      ],
    };
  }, [projectList, projectId, view, navigate, createProject]);

  return (
    <>
      <button
        type="button"
        onClick={() => setOpen(true)}
        title="Switch project (P)"
        className="-mx-1.5 flex h-7 items-center gap-1.5 rounded-md px-1.5 text-sm font-medium text-ink hover:bg-surface-2"
      >
        {projectName}
        <ChevronDown size={10} strokeWidth={1.6} className="text-muted-foreground" aria-hidden />
      </button>
      <Palette open={open} onOpenChange={setOpen} root={root} />
    </>
  );
}
