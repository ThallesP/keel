// Sign in with Axiom, browser half. The control plane makes the PKCE verifier and state
// (logSinks.beginAxiomSignIn; WebCrypto is missing on plain-http origins like a tailnet IP) and
// hands back the authorize URL. All the browser keeps is which project to return to.

const RETURN_KEY = "keel.axiom.return";

export const axiomRedirectUri = () => `${window.location.origin}/axiom/callback`;

export type AxiomReturn = { slug: string };

export function goToAxiom(url: string, back: AxiomReturn) {
  sessionStorage.setItem(RETURN_KEY, JSON.stringify(back));
  window.location.assign(url);
}

/** Project the sign-in started from, removed on read. */
export function takeAxiomReturn(): AxiomReturn | null {
  const raw = sessionStorage.getItem(RETURN_KEY);
  sessionStorage.removeItem(RETURN_KEY);
  try {
    const back = JSON.parse(raw ?? "null") as Partial<AxiomReturn> | null;
    return typeof back?.slug === "string" ? { slug: back.slug } : null;
  } catch {
    return null;
  }
}
