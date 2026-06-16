import { useEffect, useMemo, useState } from "react";
import { api } from "./api.js";
import { Header } from "./Header.jsx";
import { CommentComposer } from "./CommentComposer.jsx";
import { CheckIcon, CommentIcon, CopyIcon, GitCompareIcon, SwapIcon } from "./icons.jsx";
import { buildVersionDiff } from "./versionDiff.js";

export function VersionCompare({ planId, from, to, navigate }) {
  const [data, setData] = useState(null);
  const [err, setErr] = useState(null);
  const [mode, setMode] = useState(defaultMode);
  const [composer, setComposer] = useState(null);
  const [refreshKey, setRefreshKey] = useState(0);

  useEffect(() => {
    let cancelled = false;
    setData(null);
    setErr(null);

    api.planMeta(planId)
      .then((meta) => {
        const visible = new Set(meta.versions || []);
        if (!visible.has(from) || !visible.has(to)) {
          throw new Error("one of these versions is not available");
        }
        return Promise.all([api.versionView(planId, from), api.versionView(planId, to)])
          .then(([left, right]) => ({ meta, left, right }));
      })
      .then((next) => {
        if (!cancelled) setData(next);
      })
      .catch((e) => {
        if (!cancelled) setErr(e.message);
      });

    return () => { cancelled = true; };
  }, [planId, from, to, refreshKey]);

  useEffect(() => {
    if (data && planId.startsWith("share_") && data.left.role === "owner") {
      navigate(`/plans/${data.left.plan_id}/compare/${from}...${to}`, { replace: true });
    }
  }, [data, planId, from, to, navigate]);

  const diff = useMemo(() => {
    if (!data) return null;
    return buildVersionDiff(`v${from}`, `v${to}`, data.left.content, data.right.content);
  }, [data, from, to]);

  const commentCounts = useMemo(() => {
    if (!data) return new Map();
    return buildCommentCounts(new Map([
      [from, data.left.comments || []],
      [to, data.right.comments || []],
    ]));
  }, [data, from, to]);

  if (err) return <><Header navigate={navigate} /><main className="wrap"><p className="error">{err}</p></main></>;
  if (!data || !diff) return <><Header navigate={navigate} /><main className="wrap">Loading…</main></>;

  const versions = data.meta.versions || [];
  const title = data.meta.title || data.right.title;

  function go(nextFrom, nextTo) {
    navigate(`/plans/${planId}/compare/${nextFrom}...${nextTo}`);
  }

  function commentCount(version, line) {
    return commentCounts.get(`${version}:${line}`) || 0;
  }

  function openLineComment(e, target) {
    e.stopPropagation();
    if (!target?.line) return;
    const rect = e.currentTarget.getBoundingClientRect();
    const top = Math.min(rect.bottom + 8, window.innerHeight - 220);
    const left = Math.min(rect.left, window.innerWidth - 380);
    setComposer({
      version: target.version,
      line: target.line,
      quote: target.quote || "",
      targetLabel: `on v${target.version} line ${target.line}`,
      top: Math.max(top, 12),
      left: Math.max(left, 12),
    });
  }

  async function submitComment(body) {
    await api.addComment(planId, composer.version, {
      line_start: composer.line,
      line_end: composer.line,
      quote: composer.quote,
      body,
    });
    setComposer(null);
    setRefreshKey((n) => n + 1);
  }

  return (
    <>
      <Header navigate={navigate}>
        <span className="plan-title">{title}</span>
        <div className="versions">
          {versions.map((n) => (
            <a
              key={n}
              className={n === from || n === to ? "selected" : ""}
              href={`/plans/${planId}/v/${n}`}
              onClick={(e) => { e.preventDefault(); navigate(`/plans/${planId}/v/${n}`); }}
            >
              v{n}
            </a>
          ))}
        </div>
        <div className="compare-status" title="Comparing versions" aria-label="Comparing versions">
          <GitCompareIcon />
          <span>Compare</span>
        </div>
        <span className="spacer" />
        <PlanIdCopy planId={planId} />
      </Header>

      <main className="page compare-page">
        <div className="compare-toolbar">
          <div className="compare-selectors">
            <label>
              <span>From</span>
              <select value={from} onChange={(e) => go(Number(e.target.value), to)}>
                {versions.map((n) => <option key={n} value={n}>v{n}</option>)}
              </select>
            </label>
            <button
              className="icon-btn"
              title="Swap versions"
              aria-label="Swap versions"
              onClick={() => go(to, from)}
            >
              <SwapIcon />
            </button>
            <label>
              <span>To</span>
              <select value={to} onChange={(e) => go(from, Number(e.target.value))}>
                {versions.map((n) => <option key={n} value={n}>v{n}</option>)}
              </select>
            </label>
          </div>

          <div className="compare-open-links">
            <button onClick={() => navigate(`/plans/${planId}/v/${from}`)}>Open v{from}</button>
            <button onClick={() => navigate(`/plans/${planId}/v/${to}`)}>Open v{to}</button>
          </div>

          <div className="compare-summary" aria-label="Diff summary">
            <span className="diff-added">+{diff.stats.added}</span>
            <span className="diff-removed">-{diff.stats.removed}</span>
          </div>

          <div className="compare-mode" role="group" aria-label="Diff layout">
            <button className={mode === "unified" ? "active" : ""} onClick={() => setMode("unified")}>Unified</button>
            <button className={mode === "split" ? "active" : ""} onClick={() => setMode("split")}>Split</button>
          </div>
        </div>

        <section className={`diff-view ${mode}`}>
          {diff.hunks.length === 0
            ? <div className="diff-empty">No changes between v{from} and v{to}.</div>
            : mode === "split"
              ? <SplitDiff diff={diff} from={from} to={to} onComment={openLineComment} commentCount={commentCount} />
              : <UnifiedDiff diff={diff} from={from} to={to} onComment={openLineComment} commentCount={commentCount} />}
        </section>
      </main>

      {composer && (
        <CommentComposer
          composer={composer}
          onCancel={() => setComposer(null)}
          onSubmit={submitComment}
        />
      )}
    </>
  );
}

function PlanIdCopy({ planId }) {
  const [copied, setCopied] = useState(false);

  async function copyPlanId() {
    try {
      await navigator.clipboard.writeText(planId);
      setCopied(true);
      setTimeout(() => setCopied(false), 1200);
    } catch (_) { /* clipboard unavailable; leave the id visible */ }
  }

  return (
    <button
      type="button"
      className={`plan-id-copy mono${copied ? " copied" : ""}`}
      onClick={copyPlanId}
      title={copied ? "Copied plan id" : "Copy plan id"}
      aria-label={copied ? `Copied plan id ${planId}` : `Copy plan id ${planId}`}
    >
      <span>{planId}</span>
      {copied ? <CheckIcon /> : <CopyIcon />}
    </button>
  );
}

function defaultMode() {
  return window.matchMedia?.("(max-width: 760px)").matches ? "unified" : "split";
}

function buildCommentCounts(byVersion) {
  const out = new Map();
  for (const [version, comments] of byVersion) {
    for (const c of comments) {
      if (c.whole_file || !c.line_start) continue;
      const end = c.line_end || c.line_start;
      for (let line = c.line_start; line <= end; line++) {
        const key = `${version}:${line}`;
        out.set(key, (out.get(key) || 0) + 1);
      }
    }
  }
  return out;
}

function UnifiedDiff({ diff, from, to, onComment, commentCount }) {
  return (
    <div className="unified-diff">
      {diff.hunks.map((hunk, i) => (
        <div className="diff-hunk" key={i}>
          <div className="diff-row hunk">
            <span className="diff-ln" />
            <span className="diff-ln" />
            <span className="diff-sign" />
            <span className="diff-comment-slot" />
            <code>{hunk.header}</code>
          </div>
          {hunk.rows.flatMap((row, idx) => unifiedRows(row, idx, from, to, onComment, commentCount))}
        </div>
      ))}
    </div>
  );
}

function unifiedRows(row, idx, from, to, onComment, commentCount) {
  if (row.kind === "context") {
    const target = lineTarget(to, row.newLine, row.text);
    return [(
      <DiffLine
        key={idx}
        kind="context"
        oldLine={row.oldLine}
        newLine={row.newLine}
        sign=" "
        parts={row.parts}
        target={target}
        count={commentCount(target.version, target.line)}
        onComment={onComment}
      />
    )];
  }
  if (row.kind === "note") {
    return [<div key={idx} className="diff-row note"><span /><span /><span /><span /><code>{row.text}</code></div>];
  }
  const out = [];
  if (row.left) {
    const target = lineTarget(from, row.left.line, row.left.text);
    out.push(
      <DiffLine
        key={`${idx}-left`}
        kind="remove"
        oldLine={row.left.line}
        sign="-"
        parts={row.left.parts}
        target={target}
        count={commentCount(target.version, target.line)}
        onComment={onComment}
      />
    );
  }
  if (row.right) {
    const target = lineTarget(to, row.right.line, row.right.text);
    out.push(
      <DiffLine
        key={`${idx}-right`}
        kind="add"
        newLine={row.right.line}
        sign="+"
        parts={row.right.parts}
        target={target}
        count={commentCount(target.version, target.line)}
        onComment={onComment}
      />
    );
  }
  return out;
}

function DiffLine({ kind, oldLine, newLine, sign, parts, target, count, onComment }) {
  return (
    <div className={`diff-row ${kind}`}>
      <span className="diff-ln">{oldLine || ""}</span>
      <span className="diff-ln">{newLine || ""}</span>
      <span className="diff-sign">{sign}</span>
      <CommentButton target={target} count={count} onComment={onComment} />
      <code><InlineParts parts={parts} /></code>
    </div>
  );
}

function SplitDiff({ diff, from, to, onComment, commentCount }) {
  return (
    <div className="split-diff">
      <div className="split-head">
        <div>v{from}</div>
        <div>v{to}</div>
      </div>
      {diff.hunks.map((hunk, i) => (
        <div className="diff-hunk" key={i}>
          <div className="split-hunk-marker">{hunk.header}</div>
          {hunk.rows.map((row, idx) => (
            <SplitRow
              key={idx}
              row={row}
              from={from}
              to={to}
              onComment={onComment}
              commentCount={commentCount}
            />
          ))}
        </div>
      ))}
    </div>
  );
}

function SplitRow({ row, from, to, onComment, commentCount }) {
  if (row.kind === "context") {
    const side = { line: row.oldLine, text: row.text, kind: "context", parts: row.parts };
    const leftTarget = lineTarget(from, row.oldLine, row.text);
    const rightTarget = lineTarget(to, row.newLine, row.text);
    return (
      <div className="split-row">
        <DiffCell side={side} target={leftTarget} count={commentCount(leftTarget.version, leftTarget.line)} onComment={onComment} />
        <DiffCell side={{ ...side, line: row.newLine }} target={rightTarget} count={commentCount(rightTarget.version, rightTarget.line)} onComment={onComment} />
      </div>
    );
  }
  if (row.kind === "note") {
    return <div className="split-note">{row.text}</div>;
  }
  const leftTarget = row.left ? lineTarget(from, row.left.line, row.left.text) : null;
  const rightTarget = row.right ? lineTarget(to, row.right.line, row.right.text) : null;
  return (
    <div className={`split-row ${row.kind}`}>
      <DiffCell side={row.left} target={leftTarget} count={leftTarget ? commentCount(leftTarget.version, leftTarget.line) : 0} onComment={onComment} />
      <DiffCell side={row.right} target={rightTarget} count={rightTarget ? commentCount(rightTarget.version, rightTarget.line) : 0} onComment={onComment} />
    </div>
  );
}

function DiffCell({ side, target, count, onComment }) {
  if (!side) return <div className="diff-cell empty"><span className="diff-ln" /><span className="diff-comment-slot" /><code /></div>;
  return (
    <div className={`diff-cell ${side.kind}`}>
      <span className="diff-ln">{side.line}</span>
      <CommentButton target={target} count={count} onComment={onComment} />
      <code><InlineParts parts={side.parts} /></code>
    </div>
  );
}

function lineTarget(version, line, quote) {
  return { version, line, quote };
}

function CommentButton({ target, count, onComment }) {
  if (!target?.line) return <span className="diff-comment-slot" />;
  return (
    <button
      type="button"
      className={`diff-comment-btn${count ? " has-comments" : ""}`}
      title={count ? `${count} comment${count === 1 ? "" : "s"} on v${target.version} line ${target.line}` : `Comment on v${target.version} line ${target.line}`}
      aria-label={count ? `${count} comment${count === 1 ? "" : "s"} on v${target.version} line ${target.line}` : `Comment on v${target.version} line ${target.line}`}
      onClick={(e) => onComment(e, target)}
    >
      {count ? <span>{count}</span> : <CommentIcon />}
    </button>
  );
}

function InlineParts({ parts }) {
  return (parts || []).map((part, i) => (
    <span key={i} className={part.kind === "same" ? undefined : `diff-word ${part.kind}`}>
      {part.value}
    </span>
  ));
}
