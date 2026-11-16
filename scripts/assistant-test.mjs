import assert from "node:assert/strict";
import { assistantMarkdown } from "../web/assistant-markdown.js";

const hostile = assistantMarkdown('<img src=x onerror=alert(1)>\n**粗体**\n`<script>`\n[危险](javascript:alert(1))\n[来源](https://example.com/a?q=x&n=2)\n```\n</code><img src=x>\n```');
assert(!hostile.includes("<img") && !hostile.includes("<script"), "Markdown 不能执行原始 HTML");
assert(hostile.includes("<strong>粗体</strong>") && hostile.includes("<code>&lt;script&gt;</code>"));
assert(!hostile.includes('href="javascript:') && hostile.includes('rel="noopener noreferrer"'));
assert(assistantMarkdown("- 第一项\n- 第二项").includes("<ul><li>第一项</li><li>第二项</li></ul>"));
assert(assistantMarkdown("```\n未闭合代码").endsWith("</code></pre>"));
console.log("PASS: assistant Markdown escapes HTML, limits link protocols and closes code blocks.");
