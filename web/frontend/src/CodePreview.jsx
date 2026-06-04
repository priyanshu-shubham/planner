import { useEffect, useMemo, useRef, useState } from "react";
import { api } from "./api.js";
import { highlightCode } from "./markdown.js";

// blobCache maps sha -> content (or an in-flight Promise<content>), shared across
// every CodePreview mount. Because content is content-addressed and the HTTP
// response is immutable, the same file is fetched at most once per session and
// reused across versions and plans.
const blobCache = new Map();

function fetchBlob(sha) {
  const cached = blobCache.get(sha);
  if (cached !== undefined) return Promise.resolve(cached);
  const p = api.file(sha).then((r) => {
    blobCache.set(sha, r.content);
    return r.content;
  });
  blobCache.set(sha, p); // dedupe concurrent hovers on the same sha
  return p;
}

// CodePreview mounts once per document and event-delegates hover/focus on the
// article. On first hover of a .code-ref token it fetches the file by sha
// (cached) and shows the whole file in a syntax-highlighted popover, auto-scrolled
// to the cited lines (which are highlighted).
export function CodePreview({ docRef }) {
  const [pop, setPop] = useState(null); // {path, ranges, language, content, rect}
  const activeRef = useRef(null);
  const hideTimer = useRef(null);

  useEffect(() => {
    const el = docRef.current;
    if (!el) return;

    const cancelHide = () => {
      if (hideTimer.current) {
        clearTimeout(hideTimer.current);
        hideTimer.current = null;
      }
    };
    const scheduleHide = () => {
      cancelHide();
      hideTimer.current = setTimeout(() => {
        activeRef.current = null;
        setPop(null);
      }, 120);
    };

    async function show(token) {
      cancelHide();
      activeRef.current = token;
      const sha = token.dataset.sha;
      const meta = {
        path: token.dataset.path,
        language: token.dataset.language || "",
        ranges: JSON.parse(token.dataset.ranges || "[]"),
        rect: token.getBoundingClientRect(),
      };
      try {
        const content = await fetchBlob(sha);
        if (activeRef.current !== token) return; // pointer already moved on
        setPop({ ...meta, content });
      } catch (_) {
        if (activeRef.current === token) setPop(null);
      }
    }

    function tokenFrom(e) {
      return e.target.closest?.(".code-ref");
    }
    function onOver(e) {
      const t = tokenFrom(e);
      if (t && el.contains(t)) show(t);
    }
    function onOut(e) {
      if (tokenFrom(e)) scheduleHide();
    }
    function onFocusIn(e) {
      const t = tokenFrom(e);
      if (t && el.contains(t)) show(t);
    }
    function onFocusOut(e) {
      if (tokenFrom(e)) scheduleHide();
    }

    el.addEventListener("mouseover", onOver);
    el.addEventListener("mouseout", onOut);
    el.addEventListener("focusin", onFocusIn);
    el.addEventListener("focusout", onFocusOut);
    return () => {
      cancelHide();
      el.removeEventListener("mouseover", onOver);
      el.removeEventListener("mouseout", onOut);
      el.removeEventListener("focusin", onFocusIn);
      el.removeEventListener("focusout", onFocusOut);
    };
  }, [docRef]);

  if (!pop) return null;
  return (
    <Popover
      {...pop}
      onEnter={() => hideTimer.current && (clearTimeout(hideTimer.current), (hideTimer.current = null))}
      onLeave={() => {
        activeRef.current = null;
        setPop(null);
      }}
    />
  );
}

function Popover({ path, ranges, language, content, rect, onEnter, onLeave }) {
  const rows = useMemo(() => buildRows(content, ranges, language), [content, ranges, language]);
  // The first cited line (smallest range start); -1 for a bare path → no scroll.
  const firstCited = ranges.length ? Math.min(...ranges.map((r) => r.start)) : -1;
  const bodyRef = useRef(null);
  const citedRef = useRef(null);

  // Scroll the cited region ~1/3 down the popover body. bodyRef and citedRef both
  // resolve against the fixed .code-preview offset parent, so their offsetTop
  // difference is the row's position within the scroller — this moves only the
  // body, never the page. Bare paths keep scrollTop 0 (whole file from the top).
  useEffect(() => {
    const body = bodyRef.current;
    const row = citedRef.current;
    if (body && row) {
      body.scrollTop = row.offsetTop - body.offsetTop - body.clientHeight / 3;
    }
  }, [rows]);

  // Position below the token, clamped to the viewport.
  const top = Math.min(rect.bottom + 8, window.innerHeight - 320);
  const left = Math.min(rect.left, window.innerWidth - 1020);
  return (
    <div
      className="code-preview"
      style={{ top: Math.max(top, 12), left: Math.max(left, 12) }}
      onMouseEnter={onEnter}
      onMouseLeave={onLeave}
    >
      <div className="cp-header">
        <span className="cp-path">{path}</span>
        {ranges.length > 0 && <span className="cp-lines">{rangeLabel(ranges)}</span>}
      </div>
      <div className="cp-body" ref={bodyRef}>
        <pre className="cp-code">
          {rows.map((row) => (
            <div
              key={row.n}
              className={`cp-row${row.cited ? " cited" : ""}`}
              ref={row.n === firstCited ? citedRef : null}
            >
              <span className="cp-lineno">{row.n}</span>
              <code dangerouslySetInnerHTML={{ __html: row.html || "​" }} />
            </div>
          ))}
        </pre>
      </div>
    </div>
  );
}

// buildRows returns one row per line of the WHOLE file: { n, html, cited }. The
// file is syntax-highlighted in a single hljs pass and then split per line with
// spans preserved across newlines, so multi-line constructs (block comments,
// raw/multi-line strings) highlight correctly. `cited` marks the lines inside the
// ranges (empty for a bare path → nothing highlighted).
function buildRows(content, ranges, language) {
  const linesHtml = splitHighlightedLines(highlightCode(content, language));
  const cited = new Set();
  for (const r of ranges) {
    for (let i = r.start; i <= r.end; i++) cited.add(i);
  }
  return linesHtml.map((html, i) => ({ n: i + 1, html, cited: cited.has(i + 1) }));
}

// splitHighlightedLines splits one hljs-highlighted HTML string into per-line
// HTML without breaking spans. It scans the string tracking the stack of open
// `<span …>` tags; at each newline it closes the open spans to end the line and
// re-opens them on the next, so a span that spans lines stays balanced on both.
// hljs emits only <span>/</span> plus entity-escaped text, so a tag scanner that
// copies text runs verbatim is sufficient.
function splitHighlightedLines(html) {
  const lines = [];
  const open = []; // opening tag strings currently in effect
  let cur = "";
  let i = 0;
  while (i < html.length) {
    const c = html[i];
    if (c === "\n") {
      cur += "</span>".repeat(open.length); // close open spans for this line
      lines.push(cur);
      cur = open.join(""); // re-open them on the next line
      i += 1;
    } else if (c === "<") {
      const end = html.indexOf(">", i);
      const tag = html.slice(i, end + 1);
      if (tag.startsWith("</")) open.pop();
      else if (!tag.endsWith("/>")) open.push(tag);
      cur += tag;
      i = end + 1;
    } else {
      let j = i;
      while (j < html.length && html[j] !== "<" && html[j] !== "\n") j += 1;
      cur += html.slice(i, j);
      i = j;
    }
  }
  lines.push(cur);
  return lines;
}

// rangeLabel renders the cited ranges like "51-61, 176-222" (or "120" for a
// single line).
function rangeLabel(ranges) {
  return ranges.map((r) => (r.start === r.end ? `${r.start}` : `${r.start}-${r.end}`)).join(", ");
}
