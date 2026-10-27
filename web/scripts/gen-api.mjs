// Generates src/api/schema.d.ts from the OpenAPI document the Go server
// embeds. With --check it only compares and fails when the committed file is
// stale, which keeps the frontend types honest in CI.
import { readFileSync, writeFileSync } from "node:fs";
import { fileURLToPath, pathToFileURL } from "node:url";
import openapiTS, { astToString } from "openapi-typescript";

const spec = fileURLToPath(new URL("../../internal/api/openapi.yaml", import.meta.url));
const out = fileURLToPath(new URL("../src/api/schema.d.ts", import.meta.url));

const header = `/**
 * Generated from internal/api/openapi.yaml by \`npm run gen:api\`. Do not edit.
 */

`;

const ast = await openapiTS(pathToFileURL(spec), { alphabetize: false });
const next = header + astToString(ast);

if (process.argv.includes("--check")) {
  let current = "";
  try {
    current = readFileSync(out, "utf8");
  } catch {
    // A missing file counts as stale.
  }
  if (current !== next) {
    console.error("src/api/schema.d.ts is stale; run `npm run gen:api` in web/ and commit the result.");
    process.exit(1);
  }
  console.log("src/api/schema.d.ts is up to date.");
} else {
  writeFileSync(out, next);
  console.log("wrote src/api/schema.d.ts");
}
