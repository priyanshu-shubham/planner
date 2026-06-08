import { useEffect, useMemo } from "react";
import { renderToBlocks, detectRefs } from "./markdown.js";

// Renders markdown wide, and turns a text selection into a comment anchor:
// the selected text (quote) plus the source line range of the block(s) it spans.
// `files` is the version's referenced-file metadata list ([{path,language,sha}]);
// reference tokens whose path is in it are decorated into hoverable previews.
export function MarkdownDoc({ content, docRef, onSelect, files }) {
  const blocks = useMemo(() => renderToBlocks(content), [content]);

  // path -> {language, sha} for the snapshot-presence filter: a token decorates
  // only if its resolved path was snapshotted at post time.
  const fileMap = useMemo(() => {
    const m = new Map();
    for (const f of files || []) m.set(f.path, { language: f.language, sha: f.sha });
    return m;
  }, [files]);

  // After blocks render, draw any mermaid diagrams. Keyed on [blocks] so it only
  // runs when the parsed content changes — not on selection/hover re-renders. The
  // querySelector guard means mermaid.js (and the library) are fetched only when
  // a diagram is actually present.
  useEffect(() => {
    const el = docRef.current;
    if (el && el.querySelector("pre.mermaid:not([data-processed])")) {
      import("./mermaid.js").then((m) => m.renderMermaid(el));
    }
  }, [blocks]);

  // After blocks render, decorate `file:line` reference tokens into .code-ref
  // spans. Because each block is injected HTML (not React children), we walk the
  // text nodes ourselves. Only text nodes are split, so the .md-block datasets and
  // the select-to-comment flow stay intact. Skipping <a>/<pre> excludes links and
  // mermaid/code blocks. Keyed on [blocks, fileMap] so it re-runs when content or
  // the file list changes; unchanged blocks keep their decorations.
  useEffect(() => {
    const el = docRef.current;
    if (el && fileMap.size > 0) decorateRefs(el, fileMap);
  }, [blocks, fileMap]);

  function handleMouseUp() {
    const sel = window.getSelection();
    if (!sel || sel.rangeCount === 0 || sel.isCollapsed) return;

    const range = sel.getRangeAt(0);
    const container = docRef.current;
    if (!container || !container.contains(range.commonAncestorContainer)) return;

    // Snap the selection out to whole-word boundaries so a half-grabbed word is
    // still quoted in full, then reflect the snapped range in the on-screen
    // selection so the highlight and a manual copy match the stored quote.
    expandToWord(range);
    sel.removeAllRanges();
    sel.addRange(range);

    const quote = sel.toString().trim();
    if (!quote) return;

    const startBlock = closestBlock(range.startContainer);
    const endBlock = closestBlock(range.endContainer);
    if (!startBlock || !endBlock) return;

    const ls = Number(startBlock.dataset.lineStart) || 0;
    const le = Number(endBlock.dataset.lineEnd) || 0;
    const rect = range.getBoundingClientRect();
    onSelect({
      lineStart: Math.min(ls, le) || ls,
      lineEnd: Math.max(ls, le),
      quote: sel.toString(),
      rect,
    });
  }

  return (
    <article
      ref={docRef}
      className="markdown-body doc"
      onMouseUp={handleMouseUp}
    >
      {blocks.map((b, i) => (
        <div
          key={i}
          className="md-block"
          data-line-start={b.lineStart}
          data-line-end={b.lineEnd}
          dangerouslySetInnerHTML={{ __html: b.html }}
        />
      ))}
    </article>
  );
}

// expandToWord grows a range's endpoints outward to the nearest word boundary,
// but only when an endpoint actually falls inside a word (the boundary char and
// the char just outside it are both word characters). Endpoints that already sit
// on whitespace/punctuation are left alone, so selecting " great" never swallows
// the preceding "is". Word chars are Unicode letters, digits, and underscore, so
// accented text stays intact. Only adjusts endpoints that land in text nodes.
function expandToWord(range) {
  const isWord = (ch) => !!ch && /[\p{L}\p{N}_]/u.test(ch);

  const sc = range.startContainer;
  if (sc.nodeType === Node.TEXT_NODE) {
    const t = sc.nodeValue;
    let s = range.startOffset;
    if (isWord(t[s]) && isWord(t[s - 1])) {
      while (s > 0 && isWord(t[s - 1])) s--;
      range.setStart(sc, s);
    }
  }

  const ec = range.endContainer;
  if (ec.nodeType === Node.TEXT_NODE) {
    const t = ec.nodeValue;
    let e = range.endOffset;
    if (isWord(t[e - 1]) && isWord(t[e])) {
      while (e < t.length && isWord(t[e])) e++;
      range.setEnd(ec, e);
    }
  }
}

function closestBlock(node) {
  let el = node.nodeType === Node.TEXT_NODE ? node.parentElement : node;
  while (el && !el.classList?.contains("md-block")) el = el.parentElement;
  return el;
}

// decorateRefs walks the article's text nodes and wraps each code reference whose
// path is in fileMap into a <span class="code-ref"> carrying the metadata the
// preview popover needs (data-path, data-sha, data-language, data-ranges). Text
// nodes inside <a>/<pre> (links, code blocks, mermaid) and already-decorated
// spans are skipped.
function decorateRefs(root, fileMap) {
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT, {
    acceptNode(node) {
      for (let el = node.parentElement; el && el !== root; el = el.parentElement) {
        if (el.tagName === "A" || el.tagName === "PRE" || el.classList?.contains("code-ref")) {
          return NodeFilter.FILTER_REJECT;
        }
      }
      return NodeFilter.FILTER_ACCEPT;
    },
  });
  // Collect first; splitting nodes mid-walk would disturb the iterator.
  const targets = [];
  for (let n = walker.nextNode(); n; n = walker.nextNode()) targets.push(n);

  for (const node of targets) {
    const refs = detectRefs(node.nodeValue).filter((r) => fileMap.has(r.path));
    if (refs.length) wrapRefs(node, refs, fileMap);
  }
}

// wrapRefs replaces a single text node with a fragment interleaving its plain
// text and .code-ref spans for the given (in-order, non-overlapping) refs.
function wrapRefs(node, refs, fileMap) {
  const text = node.nodeValue;
  const frag = document.createDocumentFragment();
  let pos = 0;
  for (const r of refs) {
    if (r.index > pos) frag.appendChild(document.createTextNode(text.slice(pos, r.index)));
    const meta = fileMap.get(r.path);
    const span = document.createElement("span");
    span.className = "code-ref";
    span.textContent = r.raw;
    span.tabIndex = 0;
    span.dataset.path = r.path;
    span.dataset.sha = meta.sha;
    span.dataset.language = meta.language || "";
    span.dataset.ranges = JSON.stringify(r.ranges);
    frag.appendChild(span);
    pos = r.index + r.raw.length;
  }
  if (pos < text.length) frag.appendChild(document.createTextNode(text.slice(pos)));
  node.parentNode.replaceChild(frag, node);
}
