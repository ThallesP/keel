import { api } from "@my-better-t-app/backend/convex/_generated/api";
import { useQuery } from "convex/react";

import { useEnvironment } from "../environment";
import { Spinner } from "../primitives";
import { AxiomGate } from "./axiom-gate";
import { PageHeader } from "./chrome";
import { Explorer } from "./explorer";

/**
 * The rail's Observability page (`?view=observability`): requests and logs of the whole
 * environment in one stream, each row one click from its trace. Reads the project's Axiom sink,
 * so without one it is a gate with Sign in with Axiom (docs/logs.md). The per-service Logs tab
 * in the bottom panel keeps working on Docker either way.
 */
export function ObservabilityPage() {
  const { projectId } = useEnvironment();
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
        <PageHeader />
        <AxiomGate />
      </div>
    );
  }
  return <Explorer sink={sink} />;
}
