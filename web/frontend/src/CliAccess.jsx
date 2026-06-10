import { useEffect, useState } from "react";
import { Header } from "./Header.jsx";
import { api } from "./api.js";
import { TrashIcon } from "./icons.jsx";

function fmtDate(s) {
  if (!s) return "never";
  const d = new Date(s);
  return isNaN(d) ? s : d.toLocaleString();
}

// CliAccess lists the machines authorized to use the planner CLI for the signed-in
// user, and lets each be revoked. New entries are created by `planner setup` (or
// the /cli-setup page), so this view is management-only. Deliberately avoids the
// word "token" — in an AI tool that reads as LLM tokens.
export function CliAccess({ navigate }) {
  const [keys, setKeys] = useState(null);
  const [err, setErr] = useState(null);

  function load() {
    api.listPats().then(setKeys).catch((e) => setErr(e.message));
  }
  useEffect(() => { load(); }, []);

  async function revoke(k) {
    if (!confirm(`Revoke access for “${k.name}”? The CLI on that machine will need to run setup again.`)) return;
    try {
      await api.deletePat(k.id);
      load();
    } catch (e) {
      setErr(e.message);
    }
  }

  return (
    <>
      <Header navigate={navigate} />
      <main className="wrap">
        <div className="toolbar">
          <strong>CLI access</strong>
        </div>
        <p className="empty">
          These are the machines authorized to use the planner CLI as you. Authorize a new
          one by running <code>planner setup --server {window.location.origin}</code> on it.
        </p>
        {err && <p className="error">{err}</p>}
        {keys && keys.length === 0 && <p className="empty">No machines authorized yet.</p>}
        {keys && keys.length > 0 && (
          <ul className="plan-list">
            {keys.map((k) => (
              <li key={k.id}>
                <div className="plan-main">
                  <span className="title">{k.name}</span>
                  <span className="plan-project">
                    authorized {fmtDate(k.created_at)} · last used {fmtDate(k.last_used_at)}
                  </span>
                </div>
                <button
                  className="icon-btn danger"
                  title="Revoke access"
                  aria-label="Revoke access"
                  onClick={() => revoke(k)}
                >
                  <TrashIcon />
                </button>
              </li>
            ))}
          </ul>
        )}
      </main>
    </>
  );
}
