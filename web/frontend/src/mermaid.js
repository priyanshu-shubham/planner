// Lazy chunk: imported dynamically by MarkdownDoc only when a plan actually
// contains a `<pre class="mermaid">` block, so mermaid (~150 KB gz core + the
// diagram-type chunks it pulls on demand) never loads for diagram-free plans.
import mermaid from "mermaid";

mermaid.initialize({
  startOnLoad: false,
  theme: "default",
  // Sanitizes diagram HTML and disables click handlers; <br/> line breaks are
  // still honored. Appropriate for human/model-authored plan content.
  securityLevel: "strict",
});

// renderMermaid draws every not-yet-processed mermaid block inside `container`.
// mermaid tags rendered nodes with data-processed, and we scope the selector to
// the unprocessed ones, so repeat calls never redraw existing diagrams.
// suppressErrors leaves a malformed diagram's source visible instead of throwing.
export function renderMermaid(container) {
  const nodes = container.querySelectorAll("pre.mermaid:not([data-processed])");
  if (nodes.length === 0) return;
  mermaid.run({ nodes, suppressErrors: true });
}
