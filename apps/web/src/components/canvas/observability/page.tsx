import { useGetLogSink } from "@/api/gen";
import Loader from "@/components/loader";

import { PageHeader } from "../primitives";
import { AxiomGate } from "./axiom-gate";
import { Explorer } from "./explorer";

/**
 * The rail's Observability page (`?view=observability`): requests and logs of the whole
 * environment in one stream, each row one click from its trace. Reads the organization's Axiom
 * sink, so without one it is a gate with Sign in with Axiom (docs/logs.md). The per-service Logs
 * tab in the bottom panel keeps working on Docker either way.
 */
export function ObservabilityPage() {
  const { data } = useGetLogSink();
  if (data === undefined) {
    return (
      <div className="flex h-full flex-col bg-bg">
        <PageHeader title="Observability" />
        <div className="flex min-h-0 flex-1 flex-col">
          <Loader />
        </div>
      </div>
    );
  }
  const sink = data.sink;
  if (sink?.kind !== "axiom") {
    return (
      <div className="flex h-full flex-col bg-bg">
        <PageHeader title="Observability" />
        <AxiomGate />
      </div>
    );
  }
  return <Explorer sink={sink} />;
}
