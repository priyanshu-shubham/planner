import { useEffect, useRef, useState } from "react";
import { CheckIcon, CopyIcon } from "./icons.jsx";

export function CommentComposer({ composer, onCancel, onSubmit }) {
  const [body, setBody] = useState("");
  const [copied, setCopied] = useState(false);
  const ref = useRef(null);
  const formRef = useRef(null);
  useEffect(() => { ref.current?.focus(); }, []);

  // Copy the full (untruncated) quote. The autofocused textarea steals the
  // Cmd+C target, so a button is the only reliable way to copy the selection
  // while the composer is open.
  async function copyQuote() {
    try {
      await navigator.clipboard.writeText(composer.quote);
      setCopied(true);
      setTimeout(() => setCopied(false), 1200);
    } catch { /* clipboard unavailable; ignore */ }
  }

  // Dismiss on Escape or a click/tap outside, but only when the draft is empty,
  // so a stray click never discards typed text. Cancel always closes explicitly.
  useEffect(() => {
    const isEmpty = () => !ref.current || !ref.current.value.trim();
    function onKey(e) { if (e.key === "Escape" && isEmpty()) onCancel(); }
    function onDown(e) {
      if (formRef.current && !formRef.current.contains(e.target) && isEmpty()) onCancel();
    }
    document.addEventListener("keydown", onKey);
    document.addEventListener("mousedown", onDown);
    return () => {
      document.removeEventListener("keydown", onKey);
      document.removeEventListener("mousedown", onDown);
    };
  }, [onCancel]);

  function submit(e) {
    e.preventDefault();
    if (body.trim()) onSubmit(body.trim());
  }

  const target = composer.targetLabel || (composer.quote ? "on" : "on the whole file");
  return (
    <form ref={formRef} className="composer" style={{ top: composer.top, left: composer.left }} onSubmit={submit}>
      <div className="composer-target">
        {target}
        {composer.quote && (
          <>
            {" "}“<span className="composer-quote">{truncate(composer.quote, 80)}</span>”
            <button
              type="button"
              className="composer-copy icon-btn"
              title={copied ? "Copied" : "Copy quote"}
              aria-label={copied ? "Copied" : "Copy quote"}
              onClick={copyQuote}
            >
              {copied ? <CheckIcon /> : <CopyIcon />}
            </button>
          </>
        )}
      </div>
      <textarea ref={ref} value={body} onChange={(e) => setBody(e.target.value)} placeholder="Leave a comment for the agent…" />
      <div className="composer-row">
        <button type="button" className="subtle" onClick={onCancel}>Cancel</button>
        <button type="submit" className="primary" disabled={!body.trim()}>Add comment</button>
      </div>
    </form>
  );
}

function truncate(s, n) {
  s = s.replace(/\s+/g, " ");
  return s.length <= n ? s : s.slice(0, n - 1) + "…";
}
