import { api } from "@my-better-t-app/backend/convex/_generated/api";
import { createFileRoute, Link } from "@tanstack/react-router";
import { useQuery } from "convex/react";

import { Canvas } from "@/components/canvas/canvas";
import Loader from "@/components/loader";

/**
 * `?deployment=<id>` opens the deploy drawer for that deployment; the URL is shareable.
 * `?view=logs` is the rail's Logs page instead of the canvas.
 */
export const Route = createFileRoute("/_auth/p/$projectId")({
  component: ProjectPage,
  validateSearch: (search: Record<string, unknown>): { deployment?: string; view?: "logs" } => ({
    deployment: typeof search.deployment === "string" ? search.deployment : undefined,
    view: search.view === "logs" ? "logs" : undefined,
  }),
});

/** Resolves the slug to its production environment and hands it to the canvas. */
function ProjectPage() {
  const { projectId } = Route.useParams();
  const project = useQuery(api.projects.getBySlug, { slug: projectId });

  if (project === undefined) {
    return (
      <div className="h-svh bg-canvas">
        <Loader />
      </div>
    );
  }
  if (project === null) {
    return (
      <div className="flex h-svh flex-col items-center justify-center gap-2 bg-canvas text-sm text-muted-foreground">
        <span>Project “{projectId}” not found.</span>
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
