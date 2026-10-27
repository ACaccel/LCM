// Writes a gzip twin next to every compressible build output so that the Go
// server can answer Accept-Encoding: gzip from the embedded files without
// compressing at request time.
import { gzipSync, constants } from "node:zlib";
import { readdirSync, readFileSync, statSync, writeFileSync } from "node:fs";
import { extname, join } from "node:path";
import { fileURLToPath } from "node:url";

const dist = fileURLToPath(new URL("../../internal/webui/dist", import.meta.url));
const compressible = new Set([".js", ".css", ".html", ".svg", ".json", ".map"]);
const minBytes = 1024;

let files = 0;
let raw = 0;
let packed = 0;

function walk(dir) {
  for (const name of readdirSync(dir)) {
    const path = join(dir, name);
    if (statSync(path).isDirectory()) {
      walk(path);
      continue;
    }
    if (!compressible.has(extname(name))) continue;
    const data = readFileSync(path);
    if (data.length < minBytes) continue;
    const gz = gzipSync(data, { level: constants.Z_BEST_COMPRESSION });
    writeFileSync(`${path}.gz`, gz);
    files++;
    raw += data.length;
    packed += gz.length;
  }
}

walk(dist);
console.log(`precompress: ${files} files, ${(raw / 1024).toFixed(0)} KiB -> ${(packed / 1024).toFixed(0)} KiB gzip`);
