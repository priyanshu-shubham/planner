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
    if (lang && hljs.getLanguage(lang)) {
      try {
        return hljs.highlight(str, { language: lang }).value;
      } catch (_) {}
    }
    return "";
  },
});

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
