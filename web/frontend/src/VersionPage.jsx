import { useEffect, useRef, useState, useCallback } from "react";
import { api } from "./api.js";
import { Header } from "./Header.jsx";
import { MarkdownDoc } from "./MarkdownDoc.jsx";
import { CodePreview } from "./CodePreview.jsx";
import { TrashIcon } from "./icons.jsx";

export function VersionPage({ planId, number, navigate }) {
  const [view, setView] = useState(null);
  const [err, setErr] = useState(null);
  const [composer, setComposer] = useState(null); // {lineStart,lineEnd,quote,top,left}
  const docRef = useRef(null);

  const load = useCallback(() => {
    api.versionView(planId, number).then(setView).catch((e) => setErr(e.message));
  }, [planId, number]);

  useEffect(() => { setView(null); setErr(null); load(); }, [load]);

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
        <span className="mono">{planId}</span>
      </Header>

      <main className="page">
        {view.carryover.length > 0 && (
          <Carryover items={view.carryover} prev={view.prev_number} onChange={load} />
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
              <CommentCard key={c.id} c={c} onChange={load} onFlash={flashAnchor} />
            ))}

            {resolved.length > 0 && (
              <>
                <h2>Resolved</h2>
                {resolved.map((c) => (
                  <CommentCard key={c.id} c={c} onChange={load} onFlash={flashAnchor} />
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

function CommentCard({ c, onChange, onFlash }) {
  async function toggle() {
    if (c.status === "open") await api.resolveComment(c.id);
    else await api.reopenComment(c.id);
    onChange();
  }
  async function del() {
    if (!confirm("Delete this comment?")) return;
    await api.deleteComment(c.id);
    onChange();
  }
  return (
    <div className={`comment-card ${c.status}`} onClick={() => onFlash(c)}>
      <div className="loc">{locLabel(c)} · <span className={`status-pill ${c.status}`}>{c.status}</span></div>
      {c.quote && <blockquote className="quote">{c.quote}</blockquote>}
      <div className="body">{c.body}</div>

      {c.replies.length > 0 && (
        <div className="replies" onClick={(e) => e.stopPropagation()}>
          {c.replies.map((r) => (
            <div key={r.id} className={`reply ${r.author}`}>
              <span className="reply-author">{r.author}</span>
              <span className="reply-body">{r.body}</span>
            </div>
          ))}
        </div>
      )}

      <div onClick={(e) => e.stopPropagation()}>
        <ReplyForm commentId={c.id} onChange={onChange} />
      </div>

      <div className="actions" onClick={(e) => e.stopPropagation()}>
        <button onClick={toggle}>{c.status === "open" ? "Resolve" : "Reopen"}</button>
        <button className="icon-btn danger" title="Delete comment" aria-label="Delete comment" onClick={del}>
          <TrashIcon />
        </button>
      </div>
    </div>
  );
}

function ReplyForm({ commentId, onChange }) {
  const [open, setOpen] = useState(false);
  const [body, setBody] = useState("");
  async function send(e) {
    e.preventDefault();
    if (!body.trim()) return;
    await api.addReply(commentId, body.trim());
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

function Carryover({ items, prev, onChange }) {
  async function keep(id) { await api.keepComment(id); onChange(); }
  async function resolve(id) { await api.resolveComment(id); onChange(); }
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

function Composer({ composer, onCancel, onSubmit }) {
  const [body, setBody] = useState("");
  const ref = useRef(null);
  const formRef = useRef(null);
  useEffect(() => { ref.current?.focus(); }, []);

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
          ? <>on “<span className="composer-quote">{truncate(composer.quote, 80)}</span>”</>
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
