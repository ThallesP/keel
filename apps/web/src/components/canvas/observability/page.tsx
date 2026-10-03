import { api } from "@my-better-t-app/backend/convex/_generated/api";
import { useQuery } from "convex/react";

import { useEnvironment } from "../environment";
import { Spinner } from "../primitives";
import { AxiomGate } from "./axiom-gate";
import { PageHeader, route, type Tab } from "./chrome";
import { ProjectLogs } from "./logs";
import { TracesView } from "./traces";

/**
 * The rail's Observability page: traces and logs of the whole environment, one tab each
 * (`?view=traces|logs`). Both read the project's Axiom sink, so without one each tab is a gate
 * with Sign in with Axiom (docs/logs.md). The per-service Logs tab in the bottom panel keeps
 * working on Docker either way.
 */
export function ObservabilityPage() {
  const { projectId } = useEnvironment();
  const { view } = route.useSearch();
  const tab: Tab = view === "logs" ? "logs" : "traces";
  const sink = useQuery(api.logSinks.get, { projectId });
  if (sink === undefined) {
    return (
      <div className="flex h-full items-center justify-center bg-bg">
        <Spinner />
      </div>
    );
  }
  if (sink?.kind !== "axiom") {
    return (
      <div className="flex h-full flex-col bg-bg">
        <PageHeader tab={tab} />
        <AxiomGate tab={tab} />
      </div>
    );
  }
  return tab === "logs" ? <ProjectLogs sink={sink} /> : <TracesView sink={sink} />;
}
