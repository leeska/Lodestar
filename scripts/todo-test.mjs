import assert from "node:assert/strict";
import { createRequire } from "node:module";
import { fileURLToPath } from "node:url";
import path from "node:path";

const require = createRequire(path.join(process.env.LODESTAR_UI_DEPS || "/tmp/lodestar-ui-deps", "package.json"));
const { JSDOM } = require("jsdom");
const { build } = require("esbuild");
const root = fileURLToPath(new URL("../", import.meta.url));
const bundle = await build({ stdin: { contents: 'export { todoPage, todoForm } from "./web/admin-todo.js"; export { state } from "./web/admin-state.js";', resolveDir: root }, bundle: true, format: "iife", globalName: "Planner", write: false });
const dom = new JSDOM('<main id="admin-content"></main><dialog id="dialog"></dialog><div id="toast"></div>', { url: "http://127.0.0.1/admin", runScripts: "outside-only" });
const window = dom.window;
window.HTMLDialogElement.prototype.showModal = function () { this.open = true; };
window.HTMLDialogElement.prototype.close = function () { this.open = false; this.innerHTML = ""; };
const writes = [];
window.fetch = async (url, options = {}) => {
  if (options.method && options.method !== "GET") {
    writes.push({ url, body: JSON.parse(options.body) });
    return new Response(null, { status: 204 });
  }
  if (url.includes("/calendar/sources")) return new Response('{"sources":[]}');
  if (url.includes("/todo?")) return new Response('{"items":[]}');
  throw new Error("Unexpected request: " + url);
};
window.eval(bundle.outputFiles[0].text);
window.Planner.state.container = window.document;
window.Planner.state.tab = "todo";
await window.Planner.todoPage();
window.document.querySelector("#todo-add").click();
let form = window.document.querySelector("#todo-form");
assert.equal(typeof form.onsubmit, "function", "todo dialog must bind submission");
form.elements.title.value = "验收 <img src=x>";
form.elements.note.value = "不要导航到带有备注的 URL";
await form.onsubmit(new window.Event("submit", { cancelable: true }));
assert.equal(writes[0].url, "/api/v1/admin/todo");
assert.equal(writes[0].body.title, "验收 <img src=x>");
assert.equal(writes[0].body.due_at, 0);
window.document.querySelector("#calendar-add").click();
form = window.document.querySelector("#calendar-source-form");
assert.equal(typeof form.onsubmit, "function", "calendar dialog must bind submission");
form.elements.name.value = "日历";
form.elements.url.value = "https://example.com/private-qa.ics";
await form.onsubmit(new window.Event("submit", { cancelable: true }));
assert.equal(writes[1].url, "/api/v1/admin/calendar/sources");
assert.equal(writes[1].body.set_url, "https://example.com/private-qa.ics");
assert(!window.document.querySelector("#dialog").textContent.includes("private-qa"));
dom.window.close();
console.log("PASS: todo and private calendar dialogs submit through the API and never navigate with form values.");
