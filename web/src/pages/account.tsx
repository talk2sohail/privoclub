// Your account: the ways you sign in to Privo (ADR 0034). Linking another is
// a form post to the provider's start page, carrying the session's CSRF
// token; it comes back here. Removing one is refused for the last.

import { useEffect } from "preact/hooks";
import { api } from "../api";
import type { PageProps } from "../app";
import { invalidate, useData } from "../data";
import { navigate, query } from "../router";
import { ErrorLine, Loading, flash, useAction, when } from "../ui";

const PATH = "/me/identities";

type Identity = { provider: string; name: string; subject: string; login: string; created_at: string };
type Linkable = { kind: string; name: string; action: string; linked: boolean };

export function Account({ session, loc }: PageProps) {
  const res = useData<{ identities: Identity[]; providers: Linkable[] }>(PATH);
  const [busy, run] = useAction();

  // Linking (a provider's pages) comes back here with a result.
  const result = query(loc, "result");
  useEffect(() => {
    if (result === "linked") {
      navigate("/admin/account", { replace: true });
      flash("ok", "Linked. You can now sign in with that account too.");
    }
  }, [result]);

  const unlink = (i: Identity) =>
    void run(async () => {
      await api("POST", PATH + "/unlink", { provider: i.provider, subject: i.subject });
      invalidate(PATH);
    }, "Removed. You can no longer sign in with that account.");

  const ids = res.data?.identities;
  const only = (ids?.length ?? 0) <= 1;
  return (
    <>
      <h1>Your account</h1>
      <p class="meta">
        You are <strong>{session.user.person}</strong>. These are the accounts you sign in to Privo with. Privo never joins accounts by email: link
        another one here, while you are signed in.
      </p>
      <ErrorLine text={res.error} />
      {!ids && res.loading && <Loading what="sign-ins" />}
      {ids && (
        <section class="card">
          <h2>How you sign in</h2>
          <ul class="pins">
            {ids.map((i) => (
              <li key={i.provider + "|" + i.subject} class="pin">
                <div class="row">
                  <strong>{i.name}</strong> <span>{i.login}</span> <small class="meta">since {when(i.created_at)}</small>
                  <button
                    type="button"
                    class="link"
                    disabled={busy || only}
                    title={only ? "Your only way to sign in: link another first" : undefined}
                    onClick={() => unlink(i)}
                  >
                    Remove
                  </button>
                </div>
              </li>
            ))}
          </ul>
        </section>
      )}
      {res.data && res.data.providers.some((p) => !p.linked) && (
        <section class="card">
          <h2>Link another account</h2>
          <p class="meta">You go to the provider, sign in there, and come back here. The account must not already sign anyone in to this Privo.</p>
          {res.data.providers
            .filter((p) => !p.linked)
            .map((p) => (
              <form method="post" action={p.action} key={p.kind}>
                <input type="hidden" name="csrf" value={session.csrf} />
                <input type="hidden" name="link" value="1" />
                <button type="submit" class="primary">
                  Link {p.name}
                </button>
              </form>
            ))}
        </section>
      )}
    </>
  );
}
