// Who is signed in, the gates that pick what a route shows for them, and the account actions
// (sign in, sign up, sign out, accept an invitation) that keep the cache and the socket in step.
//
// The session is `GET /api/me` (always 200: `{user: null, organization: null}` when signed out),
// carried by the HttpOnly `keel_session` cookie the auth endpoints set. It replaces Better Auth's
// session, `api.auth.getCurrentUser`, `api.organizations.current` and convex/react's
// <Authenticated>/<Unauthenticated>/<AuthLoading> (web-data.md §3).
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useMemo } from "react";

import {
  type AcceptedInvitation,
  type Me,
  type Organization,
  type SignInRequest,
  type SignUpRequest,
  type SignedIn,
  type User,
  acceptInvitation as apiAcceptInvitation,
  signIn as apiSignIn,
  signOut as apiSignOut,
  signUp as apiSignUp,
} from "@/api/gen";

import { isApiError } from "./api";
import { cachedSession, markSignedOut, meQueryOptions, refreshSession } from "./query";
import { sessionIdentity, useRealtime } from "./realtime";

export type Session = {
  /** `undefined` while loading, `null` when signed out. */
  user: User | null | undefined;
  /** `undefined` while loading, `null` when signed out or not in the organization yet. */
  organization: Organization | null | undefined;
  /** True until the first answer (also while the server is unreachable: it keeps retrying). */
  isLoading: boolean;
  isSignedIn: boolean;
  /** The raw `GET /api/me` answer. */
  data: Me | undefined;
};

/** The signed-in user and their organization (with `role`). */
export function useSession(): Session {
  const { data } = useQuery(meQueryOptions());
  return {
    user: data ? data.user : undefined,
    organization: data ? data.organization : undefined,
    isLoading: data === undefined,
    isSignedIn: !!data?.user,
    data,
  };
}

// ── Gates ───────────────────────────────────────────────────────────────────────────────────

/**
 * Renders `children` for a signed-in user, `signedOut` for a signed-out visitor and `loading`
 * until the session is known. Signing out never navigates: the gate flips in place, so the URL
 * (`/device?user_code=…`, `/p/slug`) is still there after signing back in.
 */
export function SessionGate({
  children,
  signedOut = null,
  loading = null,
}: {
  children: React.ReactNode;
  signedOut?: React.ReactNode;
  loading?: React.ReactNode;
}) {
  const { isLoading, isSignedIn } = useSession();
  if (isLoading) return <>{loading}</>;
  return <>{isSignedIn ? children : signedOut}</>;
}

/** Drop-in for convex/react's `<Authenticated>`: children only for a signed-in user. */
export function Authenticated({ children }: { children: React.ReactNode }) {
  const { isSignedIn } = useSession();
  return isSignedIn ? <>{children}</> : null;
}

/** Drop-in for convex/react's `<Unauthenticated>`: children only once known signed out. */
export function Unauthenticated({ children }: { children: React.ReactNode }) {
  const { isLoading, isSignedIn } = useSession();
  return !isLoading && !isSignedIn ? <>{children}</> : null;
}

/** Drop-in for convex/react's `<AuthLoading>`: children until the session is known. */
export function AuthLoading({ children }: { children: React.ReactNode }) {
  const { isLoading } = useSession();
  return isLoading ? <>{children}</> : null;
}

// ── Account actions ─────────────────────────────────────────────────────────────────────────

export type AuthActions = {
  /** `POST /api/auth/sign-in`. Throws ApiError (`Invalid email or password`, …). */
  signIn: (body: SignInRequest) => Promise<SignedIn>;
  /** `POST /api/auth/sign-up` (signs in too; `invitationId` joins that invite's organization). */
  signUp: (body: SignUpRequest) => Promise<SignedIn>;
  /** `POST /api/auth/sign-out`. Never navigates; every gate flips to the sign-in form. */
  signOut: () => Promise<void>;
  /** `POST /api/invitations/{id}/accept` for the signed-in user. */
  acceptInvitation: (invitationId: string) => Promise<AcceptedInvitation>;
  /**
   * For any other write that changed who the caller is or which organization they are in (e.g.
   * `ensureDefaultProject` founding the organization): resets the cache and the socket.
   */
  refreshSession: () => Promise<Me>;
};

/**
 * Account actions. Each one, once the server answered, resets every cached answer (they were
 * scoped to the previous session or organization), refetches the session in place (no loading
 * flash) and reconnects the socket, whose channel is picked from the session at connect time.
 * Use these, never the generated `signIn` / `signUp` / `signOut` / `acceptInvitation` functions
 * or their `use…` hooks, which would leave the cache and the socket on the old session.
 */
export function useAuth(): AuthActions {
  const queryClient = useQueryClient();
  const { reconnect } = useRealtime();
  return useMemo(() => {
    const changed = async () => {
      const before = sessionIdentity(cachedSession(queryClient));
      const me = await refreshSession(queryClient);
      // The socket follows the session by itself when the user or organization changed; a new
      // session for the same identity needs an explicit reconnect.
      if (sessionIdentity(me) === before) reconnect();
      return me;
    };
    return {
      async signIn(body) {
        const res = await apiSignIn({ body }).unwrap();
        await changed();
        return res;
      },
      async signUp(body) {
        const res = await apiSignUp({ body }).unwrap();
        await changed();
        return res;
      },
      async signOut() {
        try {
          await apiSignOut({}).unwrap();
        } catch (err) {
          // Already signed out (expired session) is what we wanted; anything else is a failure.
          if (!isApiError(err) || err.status !== 401) throw err;
        }
        await markSignedOut(queryClient);
      },
      async acceptInvitation(invitationId) {
        const res = await apiAcceptInvitation({ path: { id: invitationId } }).unwrap();
        await changed();
        return res;
      },
      refreshSession: changed,
    } satisfies AuthActions;
  }, [queryClient, reconnect]);
}
