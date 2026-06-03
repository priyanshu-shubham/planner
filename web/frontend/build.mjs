// Bundles the React human-facing UI into static assets the Go binary embeds.
// Run via `npm run build`.
import * as esbuild from "esbuild";
import { mkdirSync } from "node:fs";

const outdir = "../../internal/web/static";
mkdirSync(outdir, { recursive: true });

const common = { bundle: true, minify: true, logLevel: "info" };

await esbuild.build({
  ...common,
  entryPoints: ["src/main.jsx"],
  outfile: `${outdir}/bundle.js`,
  format: "iife",
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
