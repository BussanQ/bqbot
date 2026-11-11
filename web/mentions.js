import { escapeHTML as esc } from "./markdown.js";

// Match the separators accepted by the room router. In Chinese prose the
// picker also accepts an adjacent @ and inserts the required space on selection.
const boundary = /[\s,.:;!?，。；：！？、（）【】「」()[\]{}]/u;

export function mentionAt(text, caret, end = caret) {
  if (caret !== end) return null;
  const match = /[@＠]([^\s@＠]*)$/u.exec(text.slice(0, caret));
  if (!match) return null;
  const before = text[match.index - 1];
  if (before && /[\w+-]/u.test(before)) return null;
  return { start: match.index, end: caret, query: match[1] };
}

export function mentionToken(bot, members) {
  // "@Bo Li" would also address a member called "Bo". Use the unique ID
  // whenever the router could interpret the display name as a second member.
  const name = bot.name.toLowerCase();
  const ambiguous = members.some((other) => other.id !== bot.id &&
    [other.id, other.name].some((alias) => {
      const prefix = alias.toLowerCase();
      return name === prefix || (name.startsWith(prefix) && boundary.test(name[prefix.length] || ""));
    }));
  return ambiguous ? bot.id : bot.name;
}

export function createMentionPicker(draft, list, getMembers, avatar) {
  let activeID = "", renderedRows = "", composing = false;

  function close() {
    list.hidden = true;
    activeID = "";
    draft.removeAttribute("aria-activedescendant");
  }

  function highlight(option) {
    activeID = option.dataset.member;
    for (const row of list.children)
      row.setAttribute("aria-selected", String(row === option));
    draft.setAttribute("aria-activedescendant", option.id);
    option.scrollIntoView({ block: "nearest" });
  }

  function update() {
    const range = mentionAt(draft.value, draft.selectionStart, draft.selectionEnd);
    if (draft.disabled || composing || document.activeElement !== draft || !range) return close();
    const members = getMembers();
    const query = range.query.toLowerCase();
    const matches = members.filter((b) => [b.name, b.id].some((s) => s.toLowerCase().includes(query)));
    if (!matches.length) return close();
    const rows = matches.map((b) => {
      const token = mentionToken(b, members);
      return `<div id="mention-${esc(b.id)}" role="option" data-member="${esc(b.id)}">${avatar(b)}<span>${esc(b.name)}</span><small>${esc(token === b.id ? b.id : b.engine)}</small></div>`;
    }).join("");
    if (renderedRows !== rows) {
      list.innerHTML = rows;
      renderedRows = rows;
    }
    list.hidden = false;
    highlight([...list.children].find((row) => row.dataset.member === activeID) || list.firstElementChild);
  }

  function choose(id) {
    const members = getMembers();
    const bot = members.find((b) => b.id === id);
    const range = mentionAt(draft.value, draft.selectionStart, draft.selectionEnd);
    if (!bot || !range || draft.disabled) return close();
    const preceding = draft.value[range.start - 1];
    const leading = preceding && !boundary.test(preceding) ? " " : "";
    const following = draft.value[range.end];
    const trailing = following === " " ? "" : " ";
    draft.setRangeText(leading + "@" + mentionToken(bot, members) + trailing, range.start, range.end, "end");
    if (!trailing) draft.setSelectionRange(draft.selectionEnd + 1, draft.selectionEnd + 1);
    close();
    draft.focus();
    draft.dispatchEvent(new Event("input", { bubbles: true }));
  }

  draft.addEventListener("input", update);
  draft.addEventListener("blur", close);
  draft.addEventListener("compositionstart", () => { composing = true; close(); });
  draft.addEventListener("compositionend", () => { composing = false; update(); });
  document.addEventListener("selectionchange", () => { if (!list.hidden) update(); });
  list.addEventListener("mousedown", (e) => e.preventDefault());
  list.addEventListener("click", (e) => {
    const row = e.target.closest("[data-member]");
    if (row) choose(row.dataset.member);
  });

  return {
    close,
    refresh() { if (!list.hidden) update(); },
    handleKey(e) {
      if (composing || e.isComposing || e.keyCode === 229) return true;
      if (list.hidden || e.shiftKey || e.ctrlKey || e.metaKey || e.altKey) return false;
      const rows = [...list.children];
      const index = rows.findIndex((row) => row.dataset.member === activeID);
      switch (e.key) {
        case "ArrowDown":
        case "ArrowUp":
          highlight(rows[(index + (e.key === "ArrowDown" ? 1 : -1) + rows.length) % rows.length]);
          break;
        case "Enter":
        case "Tab":
          choose(activeID);
          break;
        case "Escape":
          close();
          break;
        default:
          return false;
      }
      e.preventDefault();
      return true;
    },
  };
}
