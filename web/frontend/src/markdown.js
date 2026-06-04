// Renders markdown into discrete top-level blocks, each tagged with the source
// line range it came from. markdown-it gives every block token a
// `.map = [startLine, endLineExclusive]` (0-based); we surface that as 1-based
// inclusive line numbers so text selections can be anchored back to source lines
// (and the agent receives those line numbers via the CLI).
import MarkdownIt from "markdown-it";
import hljs from "highlight.js/lib/common";

const md = new MarkdownIt({
  html: false,
  linkify: true,
  typographer: true,
  highlight(str, lang) {
    if (lang === "mermaid") {
      // Hand the raw diagram source to mermaid (rendered client-side after
      // mount). markdown-it returns a highlight result starting with `<pre`
      // verbatim, skipping its own <pre><code> wrapper, so the source lands as
      // the element's textContent. Convert literal `\n` to <br/> to work around
      // mermaid v11's regression that renders `\n` in node labels literally
      // (mermaid-js/mermaid#1766).
      const src = str.replace(/\\n/g, "<br/>");
      return `<pre class="mermaid">${md.utils.escapeHtml(src)}</pre>`;
    }
    if (lang && hljs.getLanguage(lang)) {
      try {
        return hljs.highlight(str, { language: lang }).value;
      } catch (_) {}
    }
    return "";
  },
});

// highlightCode returns syntax-highlighted HTML for a snippet, reusing the same
// hljs instance the renderer uses. Unknown/absent languages fall back to escaped
// plain text. Used by the code-reference preview popover.
export function highlightCode(code, lang) {
  if (lang && hljs.getLanguage(lang)) {
    try {
      return hljs.highlight(code, { language: lang }).value;
    } catch (_) {}
  }
  return escapeHtml(code);
}

function escapeHtml(s) {
  return s
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;");
}

// KNOWN_BASENAMES are extensionless filenames common enough to treat as refs.
// Case-sensitive on purpose, so prose words like "license"/"todo" don't match.
const KNOWN_BASENAMES =
  "Makefile|makefile|GNUmakefile|Dockerfile|Containerfile|Jenkinsfile|Vagrantfile|Procfile|Gemfile|Rakefile|Brewfile|Justfile|Caddyfile|LICENSE|LICENCE|README|CHANGELOG|NOTICE|AUTHORS|CONTRIBUTORS|CODEOWNERS|COPYING|INSTALL|TODO";

const LINE_SPEC = ":\\d+(?:-\\d+)?(?:,\\s*\\d+(?:-\\d+)?)*";

// REF_RE matches a `file:line`-style code reference. The path is captured in
// group 1 (forms that stand on their own — extension, known extensionless
// basename, or path-separated extensionless) or group 3 (a bare extensionless
// token, which only counts when a line spec follows so prose words don't match).
// The line spec is captured as group 2 (for group-1 paths) or group 4 (group-3).
// This is the JS twin of refToken in internal/cli/refs.go — keep the two in sync.
const REF_RE = new RegExp(
  "([\\w./-]+\\.\\w+" +
    "|(?:[\\w.-]+/)*(?:" + KNOWN_BASENAMES + ")\\b" +
    "|[\\w.-]+(?:/[\\w.-]+)+)" +
    "(" + LINE_SPEC + ")?" +
    "|([\\w.-]+)(" + LINE_SPEC + ")",
  "g",
);

// detectRefs finds every code reference in text, returning
// [{ raw, path, ranges, index }] where ranges is a list of {start,end} (empty for
// a bare path) and index is the match offset in text (for DOM splitting).
export function detectRefs(text) {
  const out = [];
  for (const m of text.matchAll(REF_RE)) {
    const path = m[1] ?? m[3];
    const spec = m[2] ?? m[4] ?? "";
    out.push({ raw: m[0], path, ranges: parseRanges(spec), index: m.index });
  }
  return out;
}

// parseRanges turns a line spec (leading ":") like ":51-61, 176-222" into
// [{start:51,end:61},{start:176,end:222}]. A single line ":120" becomes
// [{start:120,end:120}]; an empty spec yields [].
function parseRanges(spec) {
  if (!spec) return [];
  const ranges = [];
  for (const part of spec.slice(1).split(",")) {
    const t = part.trim();
    if (!t) continue;
    const dash = t.indexOf("-");
    if (dash >= 0) {
      ranges.push({ start: Number(t.slice(0, dash)), end: Number(t.slice(dash + 1)) });
    } else {
      ranges.push({ start: Number(t), end: Number(t) });
    }
  }
  return ranges;
}

// renderToBlocks returns [{ html, lineStart, lineEnd }] for the given markdown.
export function renderToBlocks(src) {
  const env = {};
  const tokens = md.parse(src || "", env);
  const blocks = [];

  let i = 0;
  while (i < tokens.length) {
    const t = tokens[i];
    let slice, map;
    if (t.nesting === 1) {
      let j = i + 1;
      while (j < tokens.length && !(tokens[j].level === 0 && tokens[j].nesting === -1)) j++;
      slice = tokens.slice(i, j + 1);
      map = t.map;
      i = j + 1;
    } else {
      slice = [t];
      map = t.map;
      i += 1;
    }

    const html = md.renderer.render(slice, md.options, env);
    if (html.trim() === "") continue;

    blocks.push({
      html,
      lineStart: map ? map[0] + 1 : 0,
      lineEnd: map ? map[1] : 0,
    });
  }
  return blocks;
}
