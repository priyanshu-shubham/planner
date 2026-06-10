import { useEffect, useRef, useState, useCallback } from "react";
import { api } from "./api.js";
import { Header } from "./Header.jsx";
import { MarkdownDoc } from "./MarkdownDoc.jsx";
import { CodePreview } from "./CodePreview.jsx";
import { TrashIcon, CopyIcon, CheckIcon, CircleIcon, CheckCircleIcon, BotIcon, PersonIcon } from "./icons.jsx";

export function VersionPage({ planId, number, navigate }) {
  const [view, setView] = useState(null);
  const [err, setErr] = useState(null);
  const [composer, setComposer] = useState(null); // {lineStart,lineEnd,quote,top,left}
  const docRef = useRef(null);

  const load = useCallback(() => {
    api.versionView(planId, number).then(setView).catch((e) => setErr(e.message));
  }, [planId, number]);

  useEffect(() => { setView(null); setErr(null); load(); }, [load]);

  // The owner following their own share link is recognized server-side
  // (role "owner"); bounce them to the canonical URL.
  useEffect(() => {
    if (view && view.role === "owner" && planId.startsWith("share_")) {
      navigate(`/plans/${view.plan_id}/v/${view.number}`, { replace: true });
    }
  }, [view, planId, navigate]);

  const onSelect = useCallback(({ lineStart, lineEnd, quote, rect }) => {
    // Position the composer just below the selection, clamped to the viewport.
    const top = Math.min(rect.bottom + 8, window.innerHeight - 220);
    const left = Math.min(rect.left, window.innerWidth - 380);
    setComposer({ lineStart, lineEnd, quote, top: Math.max(top, 12), left: Math.max(left, 12) });
  }, []);

  async function submitComment(body) {
    await api.addComment(planId, number, {
      line_start: composer.lineStart,
      line_end: composer.lineEnd,
      quote: composer.quote,
      body,
    });
    setComposer(null);
    window.getSelection()?.removeAllRanges();
    load();
  }

  function commentWholeFile() {
    setComposer({ lineStart: 0, lineEnd: 0, quote: "", top: 80, left: window.innerWidth - 400 });
  }

  // Flash + scroll to the block a comment anchors to.
  function flashAnchor(c) {
    if (!docRef.current || c.whole_file) return;
    const block = [...docRef.current.querySelectorAll(".md-block")].find((b) => {
      const s = Number(b.dataset.lineStart), e = Number(b.dataset.lineEnd);
      return c.line_start >= s && c.line_start <= e;
    });
    if (block) {
      block.scrollIntoView({ behavior: "smooth", block: "center" });
      block.classList.add("flash");
      setTimeout(() => block.classList.remove("flash"), 1200);
    }
  }

  if (err) return <><Header navigate={navigate} /><main className="wrap"><p className="error">{err}</p></main></>;
  if (!view) return <><Header navigate={navigate} /><main className="wrap">Loading…</main></>;

  // Shared mode: the viewer arrived through a share link (the URL id is the
  // share id). They can read, comment, and reply; owner-only controls hide.
  const shared = view.role === "shared";

  const open = view.comments.filter((c) => c.status === "open");
  const resolved = view.comments.filter((c) => c.status === "resolved");
  const openSorted = [...open].sort((a, b) => (a.whole_file === b.whole_file ? a.line_start - b.line_start : a.whole_file ? -1 : 1));

  return (
    <>
      <Header navigate={navigate}>
        <span className="plan-title">{view.title}</span>
        <div className="versions">
          {view.versions.map((n) => (
            n === view.number
              ? <span key={n} className="current">v{n}</span>
              : <a key={n} href={`/plans/${planId}/v/${n}`} onClick={(e) => { e.preventDefault(); navigate(`/plans/${planId}/v/${n}`); }}>v{n}</a>
          ))}
        </div>
        <span className="spacer" />
        {!shared && <ShareButton planId={planId} shareId={view.share_id} onChange={load} />}
        <span className="mono">{planId}</span>
      </Header>

      <main className="page">
        {!shared && view.carryover.length > 0 && (
          <Carryover planId={planId} items={view.carryover} prev={view.prev_number} onChange={load} />
        )}

        <div className="layout">
          <MarkdownDoc content={view.content} docRef={docRef} onSelect={onSelect} files={view.files} />
          <CodePreview docRef={docRef} />

          <aside className="sidebar">
            <div className="toolbar">
              <button className="subtle" onClick={commentWholeFile}>+ Comment on whole file</button>
            </div>
            <div className="hint">Select any text in the plan to comment on it.</div>

            <h2>Open comments</h2>
            {openSorted.length === 0 && <p className="empty">No open comments.</p>}
            {openSorted.map((c) => (
              <CommentCard key={c.id} planId={planId} shared={shared} c={c} onChange={load} onFlash={flashAnchor} />
            ))}

            {resolved.length > 0 && (
              <>
                <h2>Resolved</h2>
                {resolved.map((c) => (
                  <CommentCard key={c.id} planId={planId} shared={shared} c={c} onChange={load} onFlash={flashAnchor} />
                ))}
              </>
            )}
          </aside>
        </div>
      </main>

      {composer && (
        <Composer
          composer={composer}
          onCancel={() => { setComposer(null); window.getSelection()?.removeAllRanges(); }}
          onSubmit={submitComment}
        />
      )}
    </>
  );
}

function locLabel(c) {
  if (c.whole_file) return "whole file";
  if (c.line_start === c.line_end) return `line ${c.line_start}`;
  return `lines ${c.line_start}–${c.line_end}`;
}

// AuthorBadge renders any comment/reply author as a small round badge so the
// agent and humans get one consistent treatment: the agent a bot glyph, a
// human their profile picture or initials. The full name lives in the tooltip.
function AuthorBadge({ author, name, picture }) {
  if (author === "agent") {
    return <span className="author-avatar agent-avatar" title={name ? `agent (${name})` : "agent"}><BotIcon /></span>;
  }
  // Unattributed human (no-auth mode / pre-attribution rows): a generic person.
  if (!name) return <span className="author-avatar person-avatar" title="human"><PersonIcon /></span>;
  if (picture) {
    return <img className="author-avatar" src={picture} alt={name} title={name} referrerPolicy="no-referrer" />;
  }
  const parts = name.trim().split(/\s+/).filter(Boolean);
  const initials = (parts.length >= 2 ? parts[0][0] + parts[1][0] : name.slice(0, 2)).toUpperCase();
  return <span className="author-avatar author-initials" title={name}>{initials}</span>;
}

function CommentCard({ planId, shared, c, onChange, onFlash }) {
  async function toggle() {
    if (c.status === "open") await api.resolveComment(planId, c.id);
    else await api.reopenComment(planId, c.id);
    onChange();
  }
  async function del() {
    if (!confirm("Delete this comment?")) return;
    await api.deleteComment(planId, c.id);
    onChange();
  }
  async function delReply(rid) {
    if (!confirm("Delete this reply?")) return;
    await api.deleteReply(planId, c.id, rid);
    onChange();
  }
  return (
    <div className={`comment-card ${c.status}`} onClick={() => onFlash(c)}>
      <div className="loc">
        {locLabel(c)}
        {" · "}<span className={`status-pill ${c.status}`}>{c.status}</span>
        {c.author_name && <AuthorBadge author="human" name={c.author_name} picture={c.author_picture} />}
      </div>
      {c.quote && <blockquote className="quote">{c.quote}</blockquote>}
      <div className="body">{c.body}</div>

      {c.replies.length > 0 && (
        <div className="replies" onClick={(e) => e.stopPropagation()}>
          {c.replies.map((r) => (
            <div key={r.id} className={`reply ${r.author}`}>
              <AuthorBadge author={r.author} name={r.author_name} picture={r.author_picture} />
              <span className="reply-body">{r.body}</span>
              {(!shared || r.own) && (
                <button
                  className="icon-btn danger reply-del"
                  title="Delete reply"
                  aria-label="Delete reply"
                  onClick={() => delReply(r.id)}
                >
                  <TrashIcon />
                </button>
              )}
            </div>
          ))}
        </div>
      )}

      <div onClick={(e) => e.stopPropagation()}>
        <ReplyForm planId={planId} commentId={c.id} onChange={onChange} />
      </div>

      {(!shared || c.own) && (
        <div className="actions" onClick={(e) => e.stopPropagation()}>
          {!shared && (
            <button
              className="icon-btn"
              title={c.status === "open" ? "Resolve" : "Reopen"}
              aria-label={c.status === "open" ? "Resolve comment" : "Reopen comment"}
              onClick={toggle}
            >
              {c.status === "open" ? <CheckCircleIcon /> : <CircleIcon />}
            </button>
          )}
          <button className="icon-btn danger" title="Delete comment" aria-label="Delete comment" onClick={del}>
            <TrashIcon />
          </button>
        </div>
      )}
    </div>
  );
}

function ReplyForm({ planId, commentId, onChange }) {
  const [open, setOpen] = useState(false);
  const [body, setBody] = useState("");
  async function send(e) {
    e.preventDefault();
    if (!body.trim()) return;
    await api.addReply(planId, commentId, body.trim());
    setBody("");
    setOpen(false);
    onChange();
  }
  if (!open) {
    return <button className="reply-toggle subtle" onClick={() => setOpen(true)}>Reply</button>;
  }
  return (
    <form className="reply-form" onSubmit={send}>
      <textarea value={body} onChange={(e) => setBody(e.target.value)} placeholder="Reply…" rows={2} autoFocus />
      <div className="reply-row">
        <button type="button" className="subtle" onClick={() => { setOpen(false); setBody(""); }}>Cancel</button>
        <button type="submit" className="primary" disabled={!body.trim()}>Send</button>
      </div>
    </form>
  );
}

function Carryover({ planId, items, prev, onChange }) {
  async function keep(id) { await api.keepComment(planId, id); onChange(); }
  async function resolve(id) { await api.resolveComment(planId, id); onChange(); }
  return (
    <div className="carryover">
      <h2>v{prev} has open comments — keep or resolve each on this version</h2>
      {items.map((c) => (
        <div className="co-item" key={c.id}>
          <span className="mono">{locLabel(c)}</span>
          <span className="co-body">{c.body}</span>
          <button className="primary" onClick={() => keep(c.id)}>Keep</button>
          <button onClick={() => resolve(c.id)}>Resolve</button>
        </div>
      ))}
    </div>
  );
}

// ShareButton (owner view only) manages the plan's share link: a popover with
// copy and revoke. The link is the plan URL with the share id in place of the
// plan id; anyone signed in can open it to view and comment.
function ShareButton({ planId, shareId, onChange }) {
  const [open, setOpen] = useState(false);
  const [copied, setCopied] = useState(false);
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

  async function copyLink() {
    // Idempotent: returns the existing share id when the plan is already shared.
    const { share_id } = await api.createShare(planId);
    try {
      await navigator.clipboard.writeText(`${window.location.origin}/plans/${share_id}`);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch (_) { /* clipboard unavailable; ignore */ }
    onChange(); // refresh so shareId reflects the now-active link
  }

  async function revoke() {
    await api.revokeShare(planId);
    setOpen(false);
    onChange();
  }

  return (
    <div className="share-menu" ref={ref}>
      <button
        className="setup-link"
        onClick={() => setOpen((o) => !o)}
        title="Share this plan with other users"
        aria-haspopup="menu"
        aria-expanded={open}
      >
        Share
      </button>
      {open && (
        <div className="share-dropdown" role="menu">
          <p className="share-note">
            {shareId
              ? "This plan has an active share link. Anyone signed in with it can view and comment."
              : "Create a link that lets anyone signed in view and comment on this plan."}
          </p>
          <button role="menuitem" onClick={copyLink}>
            {copied ? "Copied!" : shareId ? "Copy share link" : "Create & copy link"}
          </button>
          {shareId && (
            <button role="menuitem" className="share-revoke" onClick={revoke}>
              Revoke link
            </button>
          )}
        </div>
      )}
    </div>
  );
}

function Composer({ composer, onCancel, onSubmit }) {
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

  // Dismiss on Escape or a click/tap outside — but only when the draft is empty,
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
  return (
    <form ref={formRef} className="composer" style={{ top: composer.top, left: composer.left }} onSubmit={submit}>
      <div className="composer-target">
        {composer.quote
          ? <>
              on “<span className="composer-quote">{truncate(composer.quote, 80)}</span>”
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
          : "on the whole file"}
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
