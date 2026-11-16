import { esc } from "./api.js";

function inline(text) {
  const tokens = /`([^`\n]+)`|\*\*([^*\n]+)\*\*|\[([^\]\n]+)\]\((https?:\/\/[^\s)]+)\)/g;
  let html = "", position = 0;
  for (const match of text.matchAll(tokens)) {
    html += esc(text.slice(position, match.index));
    if (match[1]) html += `<code>${esc(match[1])}</code>`;
    else if (match[2]) html += `<strong>${esc(match[2])}</strong>`;
    else html += `<a href="${esc(match[4])}" target="_blank" rel="noopener noreferrer">${esc(match[3])}</a>`;
    position = match.index + match[0].length;
  }
  return html + esc(text.slice(position));
}

export function assistantMarkdown(text) {
  const lines = String(text || "").split("\n");
  const output = [];
  let code = null, list = false;
  const closeList = () => { if (list) { output.push("</ul>"); list = false; } };
  for (const line of lines) {
    if (/^\s*```/.test(line)) {
      closeList();
      if (code === null) code = [];
      else { output.push(`<pre><code>${esc(code.join("\n"))}</code></pre>`); code = null; }
    } else if (code !== null) code.push(line);
    else if (/^\s*[-*] /.test(line)) {
      if (!list) { output.push("<ul>"); list = true; }
      output.push(`<li>${inline(line.replace(/^\s*[-*] /, ""))}</li>`);
    } else {
      closeList();
      if (/^#{1,6} /.test(line)) output.push(`<h3>${inline(line.replace(/^#{1,6} /, ""))}</h3>`);
      else if (line.trim()) output.push(`<p>${inline(line)}</p>`);
    }
  }
  closeList();
  if (code !== null) output.push(`<pre><code>${esc(code.join("\n"))}</code></pre>`);
  return output.join("");
}
