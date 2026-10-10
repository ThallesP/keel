import { Button } from "@my-better-t-app/ui/components/button";
import { Input } from "@my-better-t-app/ui/components/input";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { toast } from "sonner";

import { useApproveDevice, useClaimDeviceCode, useDenyDevice } from "@/gen/api";
import { AuthShell } from "@/components/auth/shell";
import Loader from "@/components/loader";
import { errorMessage } from "@/lib/api";
import { useAuth, useSession } from "@/lib/session";

/**
 * Approves a `keel login` (apps/cli). The CLI prints a link to this page with its code; often an
 * agent ran it and passed the link on. Approving gives that CLI a session as the signed-in
 * account (RFC 8628 device authorization, `/api/auth/device/*`). Signed out, the _auth layout
 * shows sign-in first and keeps the URL.
 */
export const Route = createFileRoute("/_auth/device")({
  component: DevicePage,
  validateSearch: (s: Record<string, unknown>): { user_code?: string } => ({
    user_code: typeof s.user_code === "string" ? s.user_code : undefined,
  }),
});

/** As the CLI prints it: ABCD-EFGH. */
const pretty = (code: string) =>
  code.length === 8 ? `${code.slice(0, 4)}-${code.slice(4)}` : code;

function EnterCode() {
  const navigate = useNavigate();
  const [code, setCode] = useState("");
  const clean = code.replace(/[\s-]/g, "").toUpperCase();
  return (
    <form
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
  const auth = useAuth();
  const { user } = useSession();
  // Looking the code up while signed in also binds it to this account; only it can approve.
  // A write behind a GET: once per code, never retried or refetched on its own.
  const lookup = useClaimDeviceCode(
    { query: { user_code: userCode } },
    {
      query: {
        staleTime: Infinity,
        retry: false,
        refetchOnWindowFocus: false,
        meta: { realtime: false },
      },
    },
  );
  const approveDevice = useApproveDevice();
  const denyDevice = useDenyDevice();
  const [decided, setDecided] = useState<"approved" | "denied" | undefined>();
  const busy = approveDevice.isPending || denyDevice.isPending;

  const decide = async (approve: boolean) => {
    try {
      await (approve ? approveDevice : denyDevice).mutateAsync({ body: { userCode } });
    } catch (err) {
      toast.error(errorMessage(err));
      return;
    }
    setDecided(approve ? "approved" : "denied");
  };

  // The only failures here: unknown or expired code (a used one is deleted).
  const status = decided ?? (lookup.isError ? "invalid" : lookup.data?.status);
  if (status === undefined || user === undefined) return <Loader />;
  if (status === "invalid") {
    return (
      <div>
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
      <div>
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
    <div>
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
        onClick={() => void auth.signOut().catch((err: unknown) => toast.error(errorMessage(err)))}
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
    <AuthShell>
      {userCode ? <Approve key={userCode} userCode={userCode} /> : <EnterCode />}
    </AuthShell>
  );
}
