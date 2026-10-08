import { createFileRoute, Link } from "@tanstack/react-router";

import { useGetProjectBySlug } from "@/api/gen";
import { Canvas } from "@/components/canvas/canvas";
import Loader from "@/components/loader";
import { errorMessage } from "@/lib/api";

/**
 * `?deployment=<id>` opens the deploy drawer for that deployment; the URL is shareable.
 * `?view=observability` is the rail's Observability page instead of the canvas (the older
 * `?view=logs` lands there too). On it, `&trace=<id>` opens one trace full screen, and
 * `&around=<epoch ms>` the log lines around a moment (a line that names no trace).
 * `?view=settings` is the rail's Settings page.
 */
type Search = {
  deployment?: string;
  view?: "observability" | "settings";
  trace?: string;
  around?: number;
};

const OBSERVABILITY = new Set(["observability", "logs", "traces"]);

export const Route = createFileRoute("/_auth/p/$projectId")({
  component: ProjectPage,
  validateSearch: (search: Record<string, unknown>): Search => ({
    deployment: typeof search.deployment === "string" ? search.deployment : undefined,
    view: OBSERVABILITY.has(String(search.view))
      ? "observability"
      : search.view === "settings"
        ? "settings"
        : undefined,
    trace: typeof search.trace === "string" ? search.trace : undefined,
    // The router parses `around=1790…` to a number.
    around:
      typeof search.around === "number" && Number.isFinite(search.around)
        ? search.around
        : undefined,
  }),
});

/** Resolves the slug to its production environment and hands it to the canvas. */
function ProjectPage() {
  const { projectId } = Route.useParams();
  const { data, error } = useGetProjectBySlug({ path: { slug: projectId } });
  // undefined while loading; null when no such project, or not the caller's.
  const project = data?.project;

  if (project === undefined && !error) {
    return (
      <div className="h-svh bg-canvas">
        <Loader />
      </div>
    );
  }
  if (!project) {
    return (
      <div className="flex h-svh flex-col items-center justify-center gap-2 bg-canvas text-sm text-muted-foreground">
        <span>{project === null ? `Project “${projectId}” not found.` : errorMessage(error)}</span>
        <Link to="/" className="text-primary hover:underline">
          Go to your project
        </Link>
      </div>
    );
  }
  return (
    <Canvas
      key={project.environment.id}
      scope={{
        projectId: project.id,
        environmentId: project.environment.id,
        environmentName: project.environment.name,
        projectName: project.name,
      }}
    />
  );
}
