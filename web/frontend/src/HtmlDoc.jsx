import { useEffect, useMemo, useRef, useState } from "react";

// Renders an HTML plan inside a sandboxed iframe. The sandbox is `allow-scripts`
// (plus `allow-popups`/`allow-popups-to-escape-sandbox` so links can open in a
// new tab) but crucially WITHOUT `allow-same-origin`, so the agent-authored
// document runs its own JS (interactive mockups work) in an opaque origin that
// cannot reach planner's cookies, session, or API — the two together would let
// framed script escape the sandbox. Popups only let a user-clicked link open a
// new tab; they grant no access to planner's origin. Because the parent can't
// read a selection across that boundary, a tiny relay (injected below) reads the
// selection inside the frame and postMessages the quoted text + its rect up to
// us; HTML comments anchor by quote, not lines.
//
// The agent could itself postMessage a spoofed selection, but a human still has
// to write and submit the comment, and it's the agent's own plan — so the only
// consequence is a pre-filled composer, not a trust boundary.
const RELAY = `
<script>
(function () {
  function send(type, data) {
    try { parent.postMessage(Object.assign({ __planner: true, type: type }, data || {}), "*"); } catch (e) {}
  }
  // NB: doubled backslashes — this whole relay is a template literal, so \\p
  // cooks to \p, yielding the intended Unicode property escape at runtime.
  function isWord(ch) { return !!ch && /[\\p{L}\\p{N}_]/u.test(ch); }
  // Grow the selection out to whole-word boundaries so a half-grabbed word is
  // still quoted in full (mirrors expandToWord in MarkdownDoc.jsx).
  function expandToWord(range) {
    var sc = range.startContainer;
    if (sc.nodeType === 3) {
      var t = sc.nodeValue, s = range.startOffset;
      if (isWord(t[s]) && isWord(t[s - 1])) {
        while (s > 0 && isWord(t[s - 1])) s--;
        range.setStart(sc, s);
      }
    }
    var ec = range.endContainer;
    if (ec.nodeType === 3) {
      var u = ec.nodeValue, e = range.endOffset;
      if (isWord(u[e - 1]) && isWord(u[e])) {
        while (e < u.length && isWord(u[e])) e++;
        range.setEnd(ec, e);
      }
    }
  }
  function sendSelection() {
    var sel = window.getSelection && window.getSelection();
    if (!sel || sel.rangeCount === 0 || sel.isCollapsed) return;
    var range = sel.getRangeAt(0);
    expandToWord(range);
    // Reflect the snapped range back so the on-screen highlight matches the quote.
    sel.removeAllRanges();
    sel.addRange(range);
    var quote = String(sel);
    if (!quote.trim()) return;
    var r = range.getBoundingClientRect();
    send("selection", { quote: quote, rect: { top: r.top, left: r.left, bottom: r.bottom, right: r.right } });
  }
  function sendHeight() {
    // Measure the body's content height, not documentElement.scrollHeight: the
    // latter is floored at the frame's own viewport height, so the frame could
    // only ever grow, never shrink to fit shorter content.
    var body = document.body, doc = document.documentElement;
    send("height", { height: (body && body.scrollHeight) || (doc && doc.scrollHeight) || 0 });
  }
  // Capture phase, so a mockup that stops propagation on its own handlers can't
  // swallow the selection signal.
  document.addEventListener("mouseup", function () { setTimeout(sendSelection, 0); }, true);
  document.addEventListener("keyup", function (e) { if (e && e.shiftKey) setTimeout(sendSelection, 0); }, true);
  // A mousedown inside this sandboxed frame never reaches the parent document,
  // so the comment composer's outside-click dismissal can't see it. Forward it
  // up; the parent re-dispatches so that dismissal fires (see HtmlDoc's handler).
  document.addEventListener("mousedown", function () { send("pointerdown"); }, true);
  // Link handling. srcdoc iframes inherit the PARENT page's base URL, so an
  // in-page anchor like "#foo" would otherwise resolve to the planner page URL
  // and navigate the frame there — wiping the mockup. Intercept clicks:
  //   #anchor  -> scroll within the frame (stay on the page)
  //   external -> open in a new tab (leaves the doc)
  //   anything else -> suppress (nowhere for srcdoc to navigate but the parent)
  document.addEventListener("click", function (e) {
    if (e.defaultPrevented) return; // the mockup's own handler took it
    var a = e.target && e.target.closest ? e.target.closest("a[href]") : null;
    if (!a) return;
    var href = a.getAttribute("href") || "";
    if (href.charAt(0) === "#") {
      e.preventDefault();
      var id = href.slice(1);
      if (!id) { window.scrollTo(0, 0); return; }
      var t = document.getElementById(id) || document.getElementsByName(id)[0];
      if (t && t.scrollIntoView) t.scrollIntoView({ behavior: "smooth", block: "start" });
      return;
    }
    // Let modified/middle clicks fall through to native behavior for real links.
    if (e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
    if (/^(https?:|mailto:|tel:)/i.test(href)) {
      e.preventDefault();
      window.open(a.href, "_blank", "noopener");
      return;
    }
    e.preventDefault();
  }, false);
  window.addEventListener("resize", sendHeight);
  window.addEventListener("load", sendHeight);
  if (window.ResizeObserver) {
    // Observe the body so content-height changes (not just frame resizes) re-report.
    try { new ResizeObserver(sendHeight).observe(document.body || document.documentElement); } catch (e) {}
  }
  // Content may paint asynchronously; report height a few times early on.
  var n = 0, t = setInterval(function () { sendHeight(); if (++n > 20) clearInterval(t); }, 250);
  sendHeight();
})();
<\/script>`;

// withRelay injects the selection/height relay just before the document's closing
// </body> (so it runs after the plan's own markup), falling back to appending it
// when the agent's HTML has no body tag.
function withRelay(html) {
  const src = html || "";
  const i = src.lastIndexOf("</body>");
  if (i >= 0) return src.slice(0, i) + RELAY + src.slice(i);
  return src + RELAY;
}

// onResize (optional) fires whenever the framed content reports a new height, so
// a parent positioning relative to the frame — e.g. the fullscreen scroll sync —
// can re-apply as the mockup settles.
export function HtmlDoc({ content, onSelect, onResize }) {
  const iframeRef = useRef(null);
  const [height, setHeight] = useState(640);

  useEffect(() => {
    function onMessage(e) {
      const frame = iframeRef.current;
      // Only trust messages from this iframe's window (origin is opaque under the
      // sandbox, so source identity — not origin — is the check we can make).
      if (!frame || e.source !== frame.contentWindow) return;
      const d = e.data;
      if (!d || d.__planner !== true) return;

      if (d.type === "pointerdown") {
        // A click landed inside the frame. Re-dispatch it as a bubbling mousedown
        // from the iframe element so the composer's document-level outside-click
        // handler treats it as an outside click (it still honors a non-empty draft).
        frame.dispatchEvent(new MouseEvent("mousedown", { bubbles: true }));
        return;
      }
      if (d.type === "height") {
        const h = Number(d.height);
        if (Number.isFinite(h) && h > 0) {
          setHeight(Math.max(200, Math.min(Math.ceil(h), 20000)));
          if (onResize) onResize();
        }
        return;
      }
      if (d.type === "selection") {
        // Store the trimmed quote so a selection that grabbed surrounding
        // whitespace/newlines doesn't persist ragged edges.
        const quote = (d.quote || "").trim();
        if (!quote || !d.rect) return;
        // Translate the in-frame rect into parent-viewport coordinates so the
        // comment composer lands next to the selection.
        const fr = frame.getBoundingClientRect();
        const rect = {
          top: fr.top + d.rect.top,
          bottom: fr.top + d.rect.bottom,
          left: fr.left + d.rect.left,
          right: fr.left + d.rect.right,
        };
        onSelect({ lineStart: 0, lineEnd: 0, quote, rect });
      }
    }
    window.addEventListener("message", onMessage);
    return () => window.removeEventListener("message", onMessage);
  }, [onSelect, onResize]);

  const srcDoc = useMemo(() => withRelay(content), [content]);

  return (
    <article className="doc html-doc">
      <iframe
        ref={iframeRef}
        className="html-frame"
        title="Plan preview"
        sandbox="allow-scripts allow-popups allow-popups-to-escape-sandbox"
        srcDoc={srcDoc}
        style={{ height }}
      />
    </article>
  );
}
