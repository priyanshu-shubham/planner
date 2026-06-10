import { useEffect } from "react";
import { Header } from "./Header.jsx";

// Login is shown in authed mode when there is no signed-in user. It hands off to
// the server's Google OAuth start, asking to return to `next` afterward. Normally
// it redirects to Google immediately (no click needed); if a failed callback sent
// us back here with ?error=…, it shows the error and a manual retry instead — so a
// persistent failure can't bounce straight back into Google in a loop.
export function Login({ navigate, next = "/" }) {
  const params = new URLSearchParams(window.location.search);
  const error = params.get("error");
  const target = next && next.startsWith("/") && !next.startsWith("/login") ? next : "/";
  const href = `/auth/google/login?next=${encodeURIComponent(target)}`;

  useEffect(() => {
    if (!error) window.location.assign(href);
  }, [error, href]);

  return (
    <>
      <Header navigate={navigate} />
      <main className="wrap login">
        <h2>Sign in to planner</h2>
        {error ? (
          <>
            <p className="error">{error}</p>
            <a className="primary login-google" href={href}>
              Try signing in again
            </a>
          </>
        ) : (
          <p className="empty">Redirecting to Google to sign in…</p>
        )}
      </main>
    </>
  );
}
