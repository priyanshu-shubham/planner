// Bundles the React human-facing UI into static assets the Go binary embeds.
// Run via `npm run build`.
import * as esbuild from "esbuild";
import { mkdirSync, readdirSync, rmSync } from "node:fs";

const outdir = "../../internal/web/static";
mkdirSync(outdir, { recursive: true });

// Code splitting emits content-hashed chunk-*.js files; remove stale ones from
// previous builds so they don't accumulate (and get embedded as dead weight).
for (const f of readdirSync(outdir)) {
  if (f.startsWith("chunk-") && f.endsWith(".js")) rmSync(`${outdir}/${f}`);
}

const common = { bundle: true, minify: true, logLevel: "info" };

// esm + splitting lets mermaid (dynamically imported in MarkdownDoc) load as
// separate on-demand chunks instead of inflating the initial bundle. Splitting
// requires format:"esm" and an outdir (not a single outfile).
await esbuild.build({
  ...common,
  entryPoints: ["src/main.jsx"],
  outdir,
  entryNames: "bundle",
  chunkNames: "chunk-[hash]",
  format: "esm",
  splitting: true,
  jsx: "automatic",
  define: { "process.env.NODE_ENV": '"production"' },
});

await esbuild.build({
  ...common,
  entryPoints: ["src/styles.css"],
  outfile: `${outdir}/bundle.css`,
  loader: { ".woff": "dataurl", ".woff2": "dataurl", ".ttf": "dataurl" },
});

console.log("frontend bundle written to", outdir);
