// A deliberately small Markdown renderer. Raw HTML is always text; links are
// restricted to web URLs and code is escaped before being placed in the DOM.
export const escapeHTML = (value) =>
  String(value ?? "").replace(
    /[&<>"']/g,
    (c) =>
      ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[
        c
      ],
  );
export function inline(text) {
  const pieces = String(text).split(
    /(`[^`\n]+`|\[[^\]\n]+\]\(https?:\/\/[^\s)]+\))/g,
  );
  return pieces
    .map((piece) => {
      if (piece.startsWith("`") && piece.endsWith("`"))
        return `<code>${escapeHTML(piece.slice(1, -1))}</code>`;
      const link = piece.match(/^\[([^\]]+)\]\((https?:\/\/[^\s)]+)\)$/);
      if (link)
        return `<a href="${escapeHTML(link[2])}" rel="noreferrer">${escapeHTML(link[1])}</a>`;
      return escapeHTML(piece)
        .replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>")
        .replace(/\*([^*\n]+)\*/g, "<em>$1</em>")
        .replace(/~~([^~]+)~~/g, "<del>$1</del>");
    })
    .join("");
}
export function markdown(text) {
  const lines = String(text ?? "")
    .replace(/\r\n/g, "\n")
    .split("\n");
  const out = [];
  let paragraph = [],
    list = "",
    code = null,
    language = "";
  const flush = () => {
    if (paragraph.length) {
      out.push(`<p>${paragraph.map(inline).join("<br>")}</p>`);
      paragraph = [];
    }
    if (list) {
      out.push(`</${list}>`);
      list = "";
    }
  };
  const codeBlock = () =>
    `<div class="code-block"><div class="code-header"><span>${escapeHTML(language || "code")}</span><button data-copy-code>复制代码</button></div><pre><code>${escapeHTML(code.join("\n"))}</code></pre></div>`;
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    if (/^\s*```/.test(line)) {
      if (code !== null) {
        out.push(codeBlock());
        code = null;
      } else {
        flush();
        language = line.replace(/^\s*```/, "").trim();
        code = [];
      }
      continue;
    }
    if (code !== null) {
      code.push(line);
      continue;
    }
    if (!line.trim()) {
      flush();
      continue;
    }
    if (
      line.includes("|") &&
      i + 1 < lines.length &&
      /^\s*\|?\s*:?-{3,}:?\s*(\|\s*:?-{3,}:?\s*)+\|?\s*$/.test(lines[i + 1])
    ) {
      flush();
      const cells = (row) =>
        row
          .trim()
          .replace(/^\||\|$/g, "")
          .split("|")
          .map((v) => v.trim());
      out.push(
        `<div class="table-wrap"><table><thead><tr>${cells(line)
          .map((c) => `<th>${inline(c)}</th>`)
          .join("")}</tr></thead><tbody>`,
      );
      i++;
      while (
        i + 1 < lines.length &&
        lines[i + 1].includes("|") &&
        lines[i + 1].trim()
      )
        out.push(
          `<tr>${cells(lines[++i])
            .map((c) => `<td>${inline(c)}</td>`)
            .join("")}</tr>`,
        );
      out.push("</tbody></table></div>");
      continue;
    }
    const heading = line.match(/^(#{1,4})\s+(.+)$/);
    if (heading) {
      flush();
      out.push(
        `<h${heading[1].length}>${inline(heading[2])}</h${heading[1].length}>`,
      );
      continue;
    }
    if (/^\s*([-*_])\1{2,}\s*$/.test(line)) {
      flush();
      out.push("<hr>");
      continue;
    }
    if (/^>\s?/.test(line)) {
      flush();
      out.push(`<blockquote>${inline(line.replace(/^>\s?/, ""))}</blockquote>`);
      continue;
    }
    const item = line.match(/^\s*(?:([-*+])|\d+\.)\s+(.+)$/);
    if (item) {
      const type = item[1] ? "ul" : "ol";
      if (list !== type || paragraph.length) {
        flush();
        list = type;
        out.push(`<${type}>`);
      }
      out.push(`<li>${inline(item[2])}</li>`);
      continue;
    }
    if (list) flush();
    paragraph.push(line);
  }
  if (code !== null) out.push(codeBlock());
  flush();
  return out.join("");
}
