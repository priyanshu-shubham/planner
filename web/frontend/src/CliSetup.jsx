import { useState } from "react";
import { Header } from "./Header.jsx";
import { api } from "./api.js";
import { CopyIcon, CheckIcon } from "./icons.jsx";

// loopbackRedirect accepts only an http loopback URL — the local listener that
// `planner setup` opens. Anything else is refused so a minted token is never
// redirected to an arbitrary origin.
function loopbackRedirect(raw) {
  if (!raw) return null;
  try {
    const u = new URL(raw);
    if (u.protocol !== "http:") return null;
    if (!["127.0.0.1", "localhost", "[::1]"].includes(u.hostname)) return null;
    return u;
  } catch (_) {
    return null;
  }
}

// CliSetup is the browser side of `planner setup` against an authed server. The
// CLI opens it with ?redirect (its loopback callback), ?state, and ?name. The
// user confirms the machine name, a PAT is minted, and the token is handed back
// to the CLI via the loopback redirect. With no redirect it shows the token to
// copy (the headless flow).
export function CliSetup({ navigate }) {
  const params = new URLSearchParams(window.location.search);
  const redirectRaw = params.get("redirect");
  const state = params.get("state") || "";
  const redirect = redirectRaw ? loopbackRedirect(redirectRaw) : null;
  const redirectInvalid = !!redirectRaw && !redirect;

  const [name, setName] = useState(params.get("name") || "");
  const [token, setToken] = useState(null);
  const [err, setErr] = useState(null);
  const [busy, setBusy] = useState(false);
  const [copied, setCopied] = useState(false);

  async function authorize() {
    setBusy(true);
    setErr(null);
    try {
      const created = await api.createPat(name.trim() || "cli");
      if (redirect) {
        redirect.searchParams.set("token", created.token);
        redirect.searchParams.set("state", state);
        window.location.assign(redirect.toString());
        return;
      }
      setToken(created.token); // headless: show it to paste
    } catch (e) {
      setErr(e.message);
    } finally {
      setBusy(false);
    }
  }

  async function copy() {
    try {
      await navigator.clipboard.writeText(token);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch (_) {}
  }

  return (
    <>
      <Header navigate={navigate} />
      <main className="wrap">
        <h2>Authorize the planner CLI</h2>
        {redirectInvalid && (
          <p className="error">
            Refusing to authorize: the redirect target is not a local address. Start over
            with <code>planner setup --server …</code>.
          </p>
        )}
        {!redirectInvalid && !token && (
          <>
            <p className="empty">
              Give this machine a name, then authorize. A personal access token will be
              created{redirect ? " and sent back to your terminal" : " for you to copy"}.
            </p>
            <label className="filter">
              Machine name
              <input value={name} onChange={(e) => setName(e.target.value)} placeholder="my-laptop" />
            </label>
            {err && <p className="error">{err}</p>}
            <div className="modal-row">
              <button className="primary" onClick={authorize} disabled={busy}>
                {busy ? "Authorizing…" : "Authorize this machine"}
              </button>
            </div>
          </>
        )}
        {token && (
          <>
            <p className="empty">Copy this token and paste it into your terminal:</p>
            <div className="copy-box">
              <code>{token}</code>
              <button className="primary" onClick={copy}>
                {copied ? <CheckIcon /> : <CopyIcon />}
              </button>
            </div>
            <p className="modal-note">This token is shown only once.</p>
          </>
        )}
      </main>
    </>
  );
}
