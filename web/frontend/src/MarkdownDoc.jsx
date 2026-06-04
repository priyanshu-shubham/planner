import { useEffect, useMemo } from "react";
import { renderToBlocks } from "./markdown.js";

// Renders markdown wide, and turns a text selection into a comment anchor:
// the selected text (quote) plus the source line range of the block(s) it spans.
export function MarkdownDoc({ content, docRef, onSelect }) {
  const blocks = useMemo(() => renderToBlocks(content), [content]);

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

  function handleMouseUp() {
    const sel = window.getSelection();
    if (!sel || sel.rangeCount === 0 || sel.isCollapsed) return;
    const quote = sel.toString().trim();
    if (!quote) return;

    const range = sel.getRangeAt(0);
    const container = docRef.current;
    if (!container || !container.contains(range.commonAncestorContainer)) return;

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

function closestBlock(node) {
  let el = node.nodeType === Node.TEXT_NODE ? node.parentElement : node;
  while (el && !el.classList?.contains("md-block")) el = el.parentElement;
  return el;
}
