import { api } from "@my-better-t-app/backend/convex/_generated/api";
import { Button } from "@my-better-t-app/ui/components/button";
import { Input } from "@my-better-t-app/ui/components/input";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useQuery } from "convex/react";
import { useEffect, useState } from "react";
import { toast } from "sonner";

import Loader from "@/components/loader";
import { authClient } from "@/lib/auth-client";

/**
 * Approves a `keel login` (apps/cli). The CLI prints a link to this page with its code; often an
 * agent ran it and passed the link on. Approving gives that CLI a session as the signed-in
 * account (better-auth's device authorization plugin, convex/auth.ts). Signed out, the _auth
 * layout shows sign-in first and keeps the URL.
 */
export const Route = createFileRoute("/_auth/device")({
  component: DevicePage,
  validateSearch: (s: Record<string, unknown>): { user_code?: string } => ({
    user_code: typeof s.user_code === "string" ? s.user_code : undefined,
  }),
});

const card =
  "w-full max-w-sm rounded-lg border border-line bg-bg p-8 shadow-[0_1px_2px_rgba(11,18,32,0.05),0_4px_12px_rgba(11,18,32,0.04)]";

type Status = "pending" | "approved" | "denied";

/** What a failed device call says: the plugin answers in RFC 8628 shape. */
function describe(error: { error_description?: string; message?: string; statusText?: string }) {
  return error.error_description ?? error.message ?? error.statusText ?? "Something went wrong";
}

/** As the CLI prints it: ABCD-EFGH. */
const pretty = (code: string) =>
  code.length === 8 ? `${code.slice(0, 4)}-${code.slice(4)}` : code;

function EnterCode() {
  const navigate = useNavigate();
  const [code, setCode] = useState("");
  const clean = code.replace(/[\s-]/g, "").toUpperCase();
  return (
    <form
      className={card}
      onSubmit={(e) => {
        e.preventDefault();
        if (clean) void navigate({ to: "/device", search: { user_code: clean } });
      }}
    >
      <h1 className="mb-1 text-lg font-semibold tracking-tight text-ink">Sign in the keel CLI</h1>
      <p className="mb-6 text-xs text-muted-foreground">
        Enter the code <span className="font-mono">keel login</span> printed.
      </p>
      <Input
        autoFocus
        value={code}
        onChange={(e) => setCode(e.target.value)}
        placeholder="ABCD-EFGH"
        className="mb-4 font-mono uppercase"
      />
      <Button type="submit" className="w-full" disabled={!clean}>
        Continue
      </Button>
    </form>
  );
}

function Approve({ userCode }: { userCode: string }) {
  const user = useQuery(api.auth.getCurrentUser);
  // Looking the code up while signed in also binds it to this account; only it can approve.
  const [status, setStatus] = useState<Status | "invalid" | undefined>();
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    let live = true;
    void authClient.device({ query: { user_code: userCode } }).then(({ data, error }) => {
      if (!live) return;
      // The only failures here: unknown or expired code (a used one is deleted).
      setStatus(error ? "invalid" : (data.status as Status));
    });
    return () => {
      live = false;
    };
  }, [userCode]);

  const decide = async (approve: boolean) => {
    setBusy(true);
    const { error } = approve
      ? await authClient.device.approve({ userCode })
      : await authClient.device.deny({ userCode });
    setBusy(false);
    if (error) toast.error(describe(error));
    else setStatus(approve ? "approved" : "denied");
  };

  if (status === undefined || user === undefined) return <Loader />;
  if (status === "invalid") {
    return (
      <div className={card}>
        <h1 className="mb-1 text-lg font-semibold tracking-tight text-ink">Link no longer valid</h1>
        <p className="text-xs text-muted-foreground">
          It was already used, has expired, or the code is wrong. Run{" "}
          <span className="font-mono">keel login</span> again for a new link.
        </p>
      </div>
    );
  }
  if (status !== "pending") {
    return (
      <div className={card}>
        <h1 className="mb-1 text-lg font-semibold tracking-tight text-ink">
          {status === "approved" ? "CLI signed in" : "Sign-in denied"}
        </h1>
        <p className="text-xs text-muted-foreground">
          {status === "approved"
            ? "The keel CLI is signed in and can carry on. You can close this tab."
            : "The keel CLI was not signed in. You can close this tab."}
        </p>
      </div>
    );
  }
  return (
    <div className={card}>
      <h1 className="mb-1 text-lg font-semibold tracking-tight text-ink">Sign in the keel CLI</h1>
      <p className="mb-6 text-xs text-muted-foreground">
        A keel CLI, yours or an agent's, asks to sign in as {user?.email}. It gets the same access
        you have. Approve only if you started it and the code matches.
      </p>
      <div className="mb-6 rounded-md border border-line py-3 text-center font-mono text-xl tracking-[0.2em] text-ink">
        {pretty(userCode)}
      </div>
      <div className="flex gap-2">
        <Button
          variant="outline"
          className="flex-1"
          disabled={busy}
          onClick={() => void decide(false)}
        >
          Deny
        </Button>
        <Button className="flex-1" disabled={busy} onClick={() => void decide(true)}>
          Approve
        </Button>
      </div>
      <button
        type="button"
        className="mt-4 w-full text-center text-xs text-muted-foreground hover:underline"
        onClick={() => void authClient.signOut()}
      >
        Not {user?.email}? Sign out
      </button>
    </div>
  );
}

function DevicePage() {
  const { user_code } = Route.useSearch();
  const userCode = user_code?.replace(/[\s-]/g, "").toUpperCase();
  return (
    <div className="flex h-svh items-center justify-center bg-canvas">
      {userCode ? <Approve key={userCode} userCode={userCode} /> : <EnterCode />}
    </div>
  );
}
