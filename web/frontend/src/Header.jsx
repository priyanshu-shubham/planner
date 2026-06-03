import { useState } from "react";

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
      </header>
      {setupOpen && <SetupModal onClose={() => setSetupOpen(false)} />}
    </>
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
