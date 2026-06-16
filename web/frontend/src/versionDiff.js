import { diffWordsWithSpace, structuredPatch } from "diff";

const CONTEXT_LINES = 4;

export function buildVersionDiff(fromLabel, toLabel, fromText, toText) {
  const patch = structuredPatch(fromLabel, toLabel, fromText, toText, "", "", { context: CONTEXT_LINES });
  const stats = { added: 0, removed: 0 };
  const hunks = patch.hunks.map((hunk) => buildHunk(hunk, stats));
  return { hunks, stats };
}

function buildHunk(hunk, stats) {
  let oldLine = hunk.oldStart;
  let newLine = hunk.newStart;
  const rows = [];

  for (let i = 0; i < hunk.lines.length;) {
    const raw = hunk.lines[i];
    const mark = raw[0];
    const text = raw.slice(1);

    if (mark === " ") {
      rows.push({
        kind: "context",
        oldLine: oldLine++,
        newLine: newLine++,
        text,
        parts: [{ kind: "same", value: text }],
      });
      i++;
      continue;
    }

    if (mark === "-" || mark === "+") {
      const removed = [];
      const added = [];
      while (i < hunk.lines.length && (hunk.lines[i][0] === "-" || hunk.lines[i][0] === "+")) {
        const changeMark = hunk.lines[i][0];
        const changeText = hunk.lines[i].slice(1);
        if (changeMark === "-") {
          removed.push({ line: oldLine++, text: changeText });
          stats.removed++;
        } else {
          added.push({ line: newLine++, text: changeText });
          stats.added++;
        }
        i++;
      }
      rows.push(...pairChangedLines(removed, added));
      continue;
    }

    rows.push({ kind: "note", text: raw });
    i++;
  }

  return {
    oldStart: hunk.oldStart,
    oldLines: hunk.oldLines,
    newStart: hunk.newStart,
    newLines: hunk.newLines,
    header: `@@ -${formatRange(hunk.oldStart, hunk.oldLines)} +${formatRange(hunk.newStart, hunk.newLines)} @@`,
    rows,
  };
}

function pairChangedLines(removed, added) {
  const rows = [];
  const count = Math.max(removed.length, added.length);
  for (let i = 0; i < count; i++) {
    const left = removed[i] || null;
    const right = added[i] || null;
    if (left && right) {
      const { leftParts, rightParts } = changedParts(left.text, right.text);
      rows.push({
        kind: "change",
        left: { ...left, kind: "remove", parts: leftParts },
        right: { ...right, kind: "add", parts: rightParts },
      });
    } else if (left) {
      rows.push({
        kind: "remove",
        left: { ...left, kind: "remove", parts: [{ kind: "remove", value: left.text }] },
        right: null,
      });
    } else if (right) {
      rows.push({
        kind: "add",
        left: null,
        right: { ...right, kind: "add", parts: [{ kind: "add", value: right.text }] },
      });
    }
  }
  return rows;
}

function changedParts(leftText, rightText) {
  const parts = diffWordsWithSpace(leftText, rightText);
  return {
    leftParts: parts
      .filter((p) => !p.added)
      .map((p) => ({ kind: p.removed ? "remove" : "same", value: p.value })),
    rightParts: parts
      .filter((p) => !p.removed)
      .map((p) => ({ kind: p.added ? "add" : "same", value: p.value })),
  };
}

function formatRange(start, lines) {
  return lines === 1 ? String(start) : `${start},${lines}`;
}
