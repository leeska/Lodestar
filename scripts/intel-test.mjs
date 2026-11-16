import assert from "node:assert/strict";
import { createRequire } from "node:module";
import { fileURLToPath } from "node:url";
import path from "node:path";

const require = createRequire(path.join(process.env.LODESTAR_UI_DEPS || "/tmp/lodestar-ui-deps", "package.json"));
const { JSDOM } = require("jsdom");
const { build } = require("esbuild");
const root = fileURLToPath(new URL("../", import.meta.url));
const bundle = await build({ stdin: { contents: 'export { intelPage } from "./web/admin-intel.js"; export { state } from "./web/admin-state.js";', resolveDir: root }, bundle: true, format: "iife", globalName: "Intel", write: false });
const dom = new JSDOM('<main id="admin-content"></main><dialog id="dialog"></dialog><div id="toast"></div>', { url: "http://127.0.0.1/admin", runScripts: "outside-only" });
const window = dom.window;
const writes = [];
window.fetch = async (url, options = {}) => {
  if (options.method === "POST") {
    writes.push({ url, body: JSON.parse(options.body) });
    return new Response(null, { status: 204 });
  }
  let data;
  if (url.includes("/intel/sources")) data = { sources: [], connectors: [] };
  else if (url.includes("/intel/items")) data = { items: [{ id: 1, source_name: "<img src=x>", title: "<script>unsafe</script>", body: "<img src=x onerror=alert(1)>", url: "javascript:alert(1)", at: 1, read: false, starred: false, disposition: "brief" }] };
  else throw new Error("Unexpected request: " + url);
  return new Response(JSON.stringify(data), { headers: { "Content-Type": "application/json" } });
};
window.eval(bundle.outputFiles[0].text);
window.Intel.state.container = window.document;
window.Intel.state.tab = "intel";
await window.Intel.intelPage();
assert.equal(window.document.querySelector("#intel-content img"), null);
assert.equal(window.document.querySelector("#intel-content script"), null);
assert.equal(window.document.querySelector("#intel-content a"), null, "unsafe links must be omitted");
await window.document.querySelector("[data-read]").onclick(new window.Event("click"));
await window.document.querySelector("[data-star]").onclick(new window.Event("click"));
assert.deepEqual(writes, [
  { url: "/api/v1/admin/intel/items/1/mark", body: { read: true } },
  { url: "/api/v1/admin/intel/items/1/mark", body: { starred: true } },
]);
assert.equal(window.document.querySelector("[data-star]").textContent, "取消星标");
dom.window.close();
console.log("PASS: intelligence items escape feed content, reject unsafe links and preserve independent read/star state.");
