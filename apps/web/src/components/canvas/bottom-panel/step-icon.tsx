import { Check, X } from "lucide-react";

import { Spinner } from "../primitives";
import type { DeployStepStatus } from "../types";

/** 16px status glyph for deploy steps: check / cross / spinner / grey outline. */
export function StepIcon({ status }: { status: DeployStepStatus }) {
  switch (status) {
    case "done":
      return (
        <span className="flex size-4 items-center justify-center rounded-full bg-success">
          <Check size={9} strokeWidth={2.2} className="text-white" aria-hidden />
        </span>
      );
    case "failed":
      return (
        <span className="flex size-4 items-center justify-center rounded-full bg-danger">
          <X size={9} strokeWidth={2.2} className="text-white" aria-hidden />
        </span>
      );
    case "running":
      return <Spinner size={14} />;
    case "pending":
      return <span className="m-1 size-2 rounded-full border-[1.5px] border-[#C4CAD4]" />;
  }
}
