import type { Id } from "@my-better-t-app/backend/convex/_generated/dataModel";
import { createContext, useContext, type ReactNode } from "react";

export type EnvironmentScope = {
  projectId: Id<"projects">;
  environmentId: Id<"environments">;
  environmentName: string;
  projectName: string;
};

const Context = createContext<EnvironmentScope | null>(null);

export function EnvironmentProvider({
  scope,
  children,
}: {
  scope: EnvironmentScope;
  children: ReactNode;
}) {
  return <Context value={scope}>{children}</Context>;
}

/** The environment this canvas renders. Every query/mutation is scoped by it. */
export function useEnvironment(): EnvironmentScope {
  const scope = useContext(Context);
  if (!scope) throw new Error("useEnvironment must be used inside <EnvironmentProvider>");
  return scope;
}
