import { useEffect, useRef, useState } from "react";
import { auth } from "./auth.js";

export function Header({ navigate, children }) {
  const [setupOpen, setSetupOpen] = useState(false);

  return (
    <>
      <header className="topbar">
        <h1>
          <a href="/" onClick={(e) => { e.preventDefault(); navigate("/"); }}>planner</a>
        </h1>
        {children}
        <button
          className="setup-link"
          onClick={() => setSetupOpen(true)}
          title="How to point an AI agent at planner"
        >
          AI setup
        </button>
        {auth.enabled && auth.user && <UserMenu navigate={navigate} user={auth.user} />}
      </header>
      {setupOpen && <SetupModal onClose={() => setSetupOpen(false)} />}
    </>
  );
}

// initials renders at most two letters for the avatar: the first letters of the
// first two name words, else the first two characters of the name or email.
function initials(user) {
  const src = (user.name || user.email || "").trim();
  if (!src) return "?";
  const parts = src.split(/\s+/).filter(Boolean);
  if (parts.length >= 2) return (parts[0][0] + parts[1][0]).toUpperCase();
  return src.slice(0, 2).toUpperCase();
}

// UserMenu is the avatar in the top bar; clicking it opens a small menu with the
// CLI-access page and sign-out. Closes on outside click or Escape.
function UserMenu({ navigate, user }) {
  const [open, setOpen] = useState(false);
  const ref = useRef(null);

  useEffect(() => {
    if (!open) return;
    function onDown(e) { if (ref.current && !ref.current.contains(e.target)) setOpen(false); }
    function onKey(e) { if (e.key === "Escape") setOpen(false); }
    document.addEventListener("mousedown", onDown);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onDown);
      document.removeEventListener("keydown", onKey);
    };
  }, [open]);

  async function signOut() {
    await auth.logout();
    window.location.href = "/login"; // full reload resets in-memory auth state
  }

  return (
    <div className="user-menu" ref={ref}>
      <button
        className="avatar"
        onClick={() => setOpen((o) => !o)}
        title={user.email}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label="Account menu"
      >
        {user.picture
          ? <img src={user.picture} alt="" referrerPolicy="no-referrer" />
          : initials(user)}
      </button>
      {open && (
        <div className="user-dropdown" role="menu">
          <div className="user-dropdown-head">
            <div className="user-dropdown-name">{user.name || user.email}</div>
            {user.name && <div className="user-dropdown-email">{user.email}</div>}
          </div>
          <button role="menuitem" onClick={() => { setOpen(false); navigate("/settings/cli"); }}>
            CLI access
          </button>
          <button role="menuitem" onClick={signOut}>Sign out</button>
        </div>
      )}
    </div>
  );
}

function SetupModal({ onClose }) {
  const cmd = `Follow ${window.location.origin}/setup.md and set up planner`;
  const [copied, setCopied] = useState(false);

  async function copy() {
    try {
      await navigator.clipboard.writeText(cmd);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch (_) {}
  }

  return (
    <div className="modal-overlay" onClick={onClose}>
      <div className="modal" onClick={(e) => e.stopPropagation()}>
        <h3>Set up planner for your AI agent</h3>
        <p>Paste this to your coding agent (e.g. Claude Code):</p>
        <div className="copy-box">
          <code>{cmd}</code>
          <button className="primary" onClick={copy}>{copied ? "Copied" : "Copy"}</button>
        </div>
        <p className="modal-note">
          It reads the instructions at that URL and adds a “planner” usage section
          to your global <code>~/.claude/CLAUDE.md</code>.
        </p>
        <div className="modal-row">
          <button className="subtle" onClick={onClose}>Close</button>
        </div>
      </div>
    </div>
  );
}
