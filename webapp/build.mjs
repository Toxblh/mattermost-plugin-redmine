import { build } from "esbuild";
await build({
  entryPoints: ["src/index.tsx"],
  bundle: true,
  outfile: "dist/main.js",
  format: "iife",
  target: "es2020",
  minify: true,
  plugins: [
    {
      name: "host-react",
      setup(b) {
        b.onResolve({ filter: /^react$/ }, () => ({
          path: "react",
          namespace: "host",
        }));
        b.onLoad({ filter: /.*/, namespace: "host" }, () => ({
          contents: "module.exports = window.React;",
          loader: "js",
        }));
      },
    },
  ],
});
