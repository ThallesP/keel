import { api } from "@my-better-t-app/backend/convex/_generated/api";
import { getRouteApi } from "@tanstack/react-router";
import { useQuery } from "convex/react";
import { useCallback, useMemo } from "react";

import { toDeployment } from "./mapping";
import type { Deployment } from "./types";

const route = getRouteApi("/_auth/p/$projectId");

/**
 * `?deployment=<id>` is the selected deployment. Ship, node actions, the status bar and the
 * Deployments tab rows all set it; the panel opens the Deployments tab of an affected node and
 * highlights it. Reload lands on the same view.
 */
export function useDeploymentLink() {
  const { deployment: deploymentId = null } = route.useSearch();
  const navigate = route.useNavigate();

  const open = useCallback(
    (id: string) => void navigate({ search: (prev) => ({ ...prev, deployment: id }) }),
    [navigate],
  );
  const clear = useCallback(
    () => void navigate({ search: (prev) => ({ ...prev, deployment: undefined }), replace: true }),
    [navigate],
  );

  return useMemo(() => ({ deploymentId, open, clear }), [deploymentId, open, clear]);
}

/** The deployment named in the URL: undefined while loading, null when missing or not owned. */
export function useLinkedDeployment(id: string | null): Deployment | null | undefined {
  const doc = useQuery(api.deployments.get, id ? { id } : "skip");
  return useMemo(() => (doc ? toDeployment(doc) : id ? doc : null), [doc, id]);
}
