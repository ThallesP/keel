import { createContext, useContext, type ReactNode } from "react";

export type EnvironmentScope = {
  projectId: string;
  environmentId: string;
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
