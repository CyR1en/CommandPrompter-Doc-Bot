import { readdir, readFile } from "node:fs/promises";
import { extname, join, relative } from "node:path";

const root = new URL("../src/", import.meta.url);
const banned = [
  { pattern: /\b(?:localStorage|sessionStorage)\b/u, reason: "browser storage is outside the secret boundary" },
  { pattern: /\bconsole\.log\s*\(/u, reason: "shipped console logging is not permitted" },
  { pattern: /:\s*any\b|<any>|\bas\s+any\b/u, reason: "explicit any defeats the generated API boundary" },
];

async function files(directory) {
  const entries = await readdir(directory, { withFileTypes: true });
  const nested = await Promise.all(entries.map(async (entry) => {
    const path = join(directory, entry.name);
    return entry.isDirectory() ? files(path) : [path];
  }));
  return nested.flat();
}

let failed = false;
for (const path of await files(root.pathname)) {
  if (![".ts", ".tsx"].includes(extname(path)) || path.endsWith("schema.d.ts")) continue;
  const source = await readFile(path, "utf8");
  for (const rule of banned) {
    if (rule.pattern.test(source)) {
      failed = true;
      process.stderr.write(`${relative(root.pathname, path)}: ${rule.reason}\n`);
    }
  }
}

if (failed) process.exitCode = 1;
