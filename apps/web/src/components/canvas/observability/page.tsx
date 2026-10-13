import { useQueryErrorResetBoundary } from "@tanstack/react-query";
import { Suspense, useState } from "react";
import { ErrorBoundary } from "react-error-boundary";

import Loader from "@/components/loader";
import { errorMessage } from "@/lib/api";

import { PageHeader } from "../primitives";
import { RangePicker, type TimeRange } from "../time-range";
import { Dashboard } from "./dashboard";

export function ObservabilityPage() {
  const [range, setRange] = useState<TimeRange>("1h");
  const [editing, setEditing] = useState(false);
  const { reset } = useQueryErrorResetBoundary();
  return (
    <div className="flex h-full flex-col bg-bg">
      <PageHeader title="Observability">
        <RangePicker value={range} onChange={setRange} />
        <button
          type="button"
          aria-pressed={editing}
          onClick={() => setEditing(!editing)}
          className="flex h-7 items-center rounded-md border border-line px-2.5 text-2xs text-muted-foreground hover:text-ink aria-pressed:border-primary aria-pressed:text-primary"
        >
          {editing ? "Done" : "Customize"}
        </button>
      </PageHeader>
      <div className="min-h-0 flex-1 overflow-auto">
        <ErrorBoundary
          onReset={reset}
          fallbackRender={({ error, resetErrorBoundary }) => (
            <p className="px-5 py-4 text-xs text-danger">
              {errorMessage(error)}{" "}
              <button type="button" className="underline" onClick={resetErrorBoundary}>
                Retry
              </button>
            </p>
          )}
        >
          <Suspense fallback={<Loader />}>
            <Dashboard range={range} editing={editing} />
          </Suspense>
        </ErrorBoundary>
      </div>
    </div>
  );
}
