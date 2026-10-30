import { escapeHTML as esc, markdown } from "./markdown.js";
import { createToolsUI } from "./tools.js";
import { createRemoteUI } from "./remote.js";
import { createTeamUI } from "./team.js";
import { createVoiceUI } from "./voice.js";
import { createComputerUI } from "./computer.js";

const paths = {
  plus: "M12 5v14M5 12h14",
  search: "M21 21l-5-5M18 10a8 8 0 1 1-16 0 8 8 0 0 1 16 0",
  settings:
    "M12 8a4 4 0 1 0 0 8 4 4 0 0 0 0-8M9 3l-1 3-3 1-2 4 2 2v4l4 3 3-1 3 1 4-3v-4l2-2-2-4-3-1-1-3z",
  sun: "M12 8a4 4 0 1 0 0 8 4 4 0 0 0 0-8M12 2v2M12 20v2M2 12h2M20 12h2M5 5l1 1M18 18l1 1M5 19l1-1M18 6l1-1",
  moon: "M20 15A9 9 0 0 1 9 4 9 9 0 1 0 20 15",
  grid: "M3 3h7v7H3zM14 3h7v7h-7zM3 14h7v7H3zM14 14h7v7h-7z",
  compose:
    "M14 4H5a2 2 0 0 0-2 2v13a2 2 0 0 0 2 2h13a2 2 0 0 0 2-2v-9M17 3l4 4-10 10-5 1 1-5z",
  panel:
    "M4 3h16a1 1 0 0 1 1 1v16a1 1 0 0 1-1 1H4a1 1 0 0 1-1-1V4a1 1 0 0 1 1-1M15 3v18",
  chevron: "m8 10 4 4 4-4",
  up: "M12 19V5m-6 6 6-6 6 6",
  down: "M12 5v14m-6-6 6 6 6-6",
  stop: "M6 6h12v12H6z",
  close: "m6 6 12 12M18 6 6 18",
  spark: "m12 3 2.5 6.5L21 12l-6.5 2.5L12 21l-2.5-6.5L3 12l6.5-2.5z",
  code: "m8 6-6 6 6 6m8-12 6 6-6 6m-3-15-2 18",
  pen: "m16 3 5 5-12 12H4v-5zM13 6l5 5",
  leaf: "M20 3C5 2 1 10 6 16s15 1 14-13M5 21l10-12",
  lock: "M6 10h12v11H6zM8 10V6a4 4 0 0 1 8 0v4",
  chat: "M21 11a9 8 0 0 1-9 8H7l-5 3 1-7a8 8 0 0 1-1-4 9 8 0 0 1 19 0",
  hash: "M5 9h14M4 15h14M10 3 8 21M16 3l-2 18",
  dots: "M5 12h.01M12 12h.01M19 12h.01",
  copy: "M8 8h13v13H8zM16 8V3H3v13h5",
  folder: "M3 5h6l2 3h10v12H3z",
  memory: "M5 3h14v18H5zM9 7h6M9 11h6M9 15h4",
  pin: "m9 3 9 3-2 6 2 4-6-2-3 3-2-6zM8 16l-5 5",
  archive: "M4 8h16v13H4zM2 3h20v5H2zM9 12h6",
  user: "M12 3a4 4 0 1 0 0 8 4 4 0 0 0 0-8M4 21v-3a8 5 0 0 1 16 0v3",
};
const icon = (name) =>
  `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="${paths[name] || paths.chat}"/></svg>`;
const $ = (id) => document.getElementById(id);
for (const el of document.querySelectorAll("[data-icon]"))
  el.innerHTML = icon(el.dataset.icon);
const bridge = (method, ...args) => {
  if (!window.mygo)
    return Promise.reject(new Error("请通过 bqbot 桌面应用打开此界面。"));
  return window.mygo.call("Desktop." + method, ...args);
};
const action = (payload) => bridge("Action", payload);
let state = { bots: [], rooms: [], threads: {}, engines: [], unread: {} },
  conversationKey = "",
  showHidden = false,
  showClosed = false,
  detailsOpen = false;
const workDirs = new Map();
let refreshing = false,
  refreshAgain = false,
  refreshTimer,
  toastTimer,
  dialogGeneration = 0,
  messageSignature = "",
  navSignature = "",
  approvalSignature = "";
const drafts = new Map(),
  imageDrafts = new Map(),
  sending = new Set();
const botByID = (id) => (state.bots || []).find((b) => b.id === id);
const currentBot = () => botByID(state.conversation?.bot_id || state.selected);
const currentRoom = () =>
  (state.rooms || []).find((r) => r.id === state.conversation?.room_id);
const currentMeta = () =>
  Object.values(state.threads || {})
    .flatMap((x) => x || [])
    .find((t) => t.id === state.conversation?.id);
const scope = (c) => ({
  id: c.room_id || c.id,
  scope: c.room_id ? "room" : "thread",
});
const avatar = (b = {}, large = false) =>
  `<span class="avatar${large ? " large" : ""}" data-engine="${esc(b.engine || "echo")}">${esc(b.avatar || (b.name || "B").slice(0, 1).toUpperCase())}</span>`;
const niceTitle = (t) =>
  t.title === "Main" ? "默认对话" : t.title || "新对话";
const time = (at) =>
  at
    ? new Date(at).toLocaleTimeString("zh-CN", {
        hour: "2-digit",
        minute: "2-digit",
      })
    : "";
function notify(text) {
  $("toast").textContent = text;
  $("toast").hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => ($("toast").hidden = true), 3500);
}
function showError(error) {
  notify(error.message || String(error));
}
function run(fn) {
  return (...args) => {
    try {
      return Promise.resolve(fn(...args)).catch(showError);
    } catch (error) {
      showError(error);
    }
  };
}
function scheduleRefresh() {
  clearTimeout(refreshTimer);
  refreshTimer = setTimeout(() => refresh().catch(showError), 75);
}
async function refresh() {
  if (refreshing) {
    refreshAgain = true;
    return;
  }
  refreshing = true;
  try {
    do {
      refreshAgain = false;
      state = await bridge("State");
      render();
      voiceUI.onConversation(state).catch(showError);
    } while (refreshAgain);
  } finally {
    refreshing = false;
  }
}
async function mutate(payload) {
  const result = await action(payload);
  await refresh();
  return result;
}
function nearBottom() {
  const el = $("message-scroll");
  return el.scrollHeight - el.scrollTop - el.clientHeight < 90;
}
function bottom() {
  const el = $("message-scroll");
  const c = state.conversation;
  el.scrollTop =
    c?.messages?.length || c?.pending?.length || c?.busy ? el.scrollHeight : 0;
  $("jump-bottom").hidden = true;
}
function resizeDraft() {
  $("draft").style.height = "auto";
  $("draft").style.height = Math.min(150, $("draft").scrollHeight) + "px";
}

function render() {
  const c = state.conversation,
    key = c?.id || "",
    changed = key !== conversationKey;
  if (changed) {
    conversationKey = key;
    $("draft").value = drafts.get(key) || "";
    resizeDraft();
    messageSignature = "";
    approvalSignature = "";
    $("messages").replaceChildren();
  }
  renderNavigation();
  renderHeader();
  renderWelcome();
  renderMessages(changed);
  renderApprovals();
  renderComposer();
  renderInspector();
  $("status").hidden = !state.status;
  if (state.status)
    $("status").innerHTML =
      `<span>${esc(state.status)}</span><button data-dismiss aria-label="关闭提示">${icon("close")}</button>`;
  $("new-thread").disabled = !currentBot() || !!c?.room_id;
  $("details").disabled = !c;
  if (changed) requestAnimationFrame(bottom);
}
function renderNavigation() {
  const sig = JSON.stringify([
    state.bots,
    state.rooms,
    state.threads,
    state.unread,
    conversationKey,
    showHidden,
    showClosed,
  ]);
  if (sig === navSignature) return;
  navSignature = sig;
  const bots = [...(state.bots || [])]
    .filter((b) => showHidden || !b.hidden)
    .sort((a, b) => Number(!!b.pinned) - Number(!!a.pinned));
  $("bot-count").textContent = bots.length;
  $("bots").innerHTML = bots.length
    ? bots
        .map((b) => {
          const selected =
            state.conversation?.bot_id === b.id && !state.conversation?.room_id;
          const unread =
            b.unread ||
            (state.threads[b.id] || []).some((t) => state.unread[t.id] > 0);
          const threads = selected
            ? (state.threads[b.id] || []).filter((t) => showClosed || !t.closed)
            : [];
          return `<button class="agent-row ${selected ? "selected" : ""}" data-select="${esc(b.id)}" aria-current="${selected ? "page" : "false"}">${avatar(b)}<span class="agent-label"><span class="agent-name">${esc(b.name)}${b.pinned ? " ·" : ""}</span><span class="agent-meta">${esc(b.engine)}${b.hidden ? " · 已隐藏" : ""}</span></span>${unread ? '<span class="nav-dot" aria-label="未读"></span>' : ""}</button>${threads.length > 1 ? threads.map((t) => `<button class="thread-row ${conversationKey === t.id ? "active" : ""}" data-select="${esc(t.id)}">${icon(t.closed ? "archive" : "chat")}${esc(niceTitle(t))}${t.asker_id ? " ↗" : ""}</button>`).join("") : ""}`;
        })
        .join("")
    : '<p class="nav-empty">添加一位伙伴，<br>开启你的第一段对话。</p>';
  $("rooms").innerHTML =
    (state.rooms || [])
      .map(
        (r) =>
          `<button class="room-row ${state.conversation?.room_id === r.id ? "selected" : ""}" data-room="${esc(r.id)}">${icon("hash")}<span class="agent-label">${esc(r.name)}</span>${state.unread["room:" + r.id] ? '<span class="nav-dot"></span>' : ""}</button>`,
      )
      .join("") || '<p class="nav-empty">让多位伙伴一起参与讨论。</p>';
  $("show-hidden").textContent = showHidden
    ? "收起隐藏伙伴"
    : "显示已隐藏的伙伴";
}
function renderHeader() {
  const b = currentBot(),
    r = currentRoom(),
    c = state.conversation,
    meta = currentMeta();
  const title = r?.name || b?.name || "你的 AI 工作空间";
  $("breadcrumb").textContent = r
    ? "协作空间"
    : meta && meta.id !== meta.bot_id
      ? niceTitle(meta)
      : title;
  $("conversation-header").innerHTML =
    `${r ? `<span class="avatar">${icon("hash")}</span>` : avatar(b || { name: "B" })}<div><h1>${esc(title)}</h1><p><span class="status-dot"></span>${c?.closed ? "只读会话" : c?.busy ? "正在处理" : r ? `${r.members.length} 位伙伴 · ${r.reply_mode === "auto" ? "自动分配" : "负责人模式"}` : b ? `${esc(b.engine)} · ${esc(b.model || "默认模型")}` : "与灵感对话，和伙伴协作"}</p></div><div class="header-actions">${c ? `<button class="icon-button" data-menu aria-label="更多操作">${icon("dots")}</button>` : ""}</div>`;
}
function renderWelcome() {
  const c = state.conversation,
    b = currentBot(),
    r = currentRoom();
  const empty = !(c?.messages?.length || c?.pending?.length || c?.busy);
  $("welcome").hidden = !empty;
  if (!empty) return;
  $("welcome").innerHTML =
    `<div class="welcome-emblem">${icon("spark")}</div><span class="eyebrow">A LITTLE SPACE FOR BIG IDEAS</span><h2>${b || r ? "新的对话，" : "你好，"}<br><em>${b || r ? "从一个想法开始。" : "让好想法发生。"}</em></h2><p>${r ? `这里是 ${esc(r.name)}。让不同的视角，在同一处相遇。` : b ? `和 ${esc(b.name)} 一起写作、探索、构建。<br>不必想得完整，从你此刻的问题开始。` : "把你信任的 AI 伙伴聚在一起。<br>一个安静、专注、属于你的工作空间。"}</p><div class="suggestions"><button class="suggestion" data-prompt="帮我梳理一个想法。请先问我几个问题，了解我的目标和目前的思路。">${icon("leaf")}<strong>整理一个想法</strong><small>让零散的灵感，有了方向</small></button><button class="suggestion" data-prompt="帮我写一份清晰、自然的内容。请先了解我的主题、读者和希望表达的观点。">${icon("pen")}<strong>一起写点什么</strong><small>把心里的话，好好说出来</small></button><button class="suggestion" data-prompt="帮我分析一个代码问题。请先询问技术栈、预期行为和我遇到的问题。">${icon("code")}<strong>解决一个问题</strong><small>从复杂的问题，找到下一步</small></button></div><div class="welcome-footnote">${icon("lock")}对话保存在本机 · 模型请求由所选引擎处理</div>`;
}
function messageHTML(m) {
  const b = botByID(m.bot_id) || currentBot() || { name: "伙伴" };
  return `${m.role === "user" ? `<span class="avatar">${icon("user")}</span>` : avatar(b)}<div class="message-content"><div class="message-heading"><strong>${m.role === "user" ? "你" : esc(b.name)}</strong><time>${time(m.at)}</time><button class="icon-button" data-copy-message="${esc(m.id)}" aria-label="复制消息">${icon("copy")}</button>${m.role === "assistant" ? `<button class="model-button" data-speak-message="${esc(m.id)}" aria-label="朗读这条回复">朗读</button>` : ""}</div><div class="markdown">${markdown(m.text)}</div>${messageImagesHTML(m.attachments)}${m.tools?.length ? `<details class="tool-lines"><summary>工具与协作 · ${m.tools.length} 项活动</summary><ul>${m.tools.map((t) => `<li>${esc(t)}</li>`).join("")}</ul></details>` : ""}</div>`;
}
function messageImagesHTML(images = []) {
  const screenshots = images.filter((a) => a.name?.startsWith("computer-"));
  const attachments = images.filter((a) => !a.name?.startsWith("computer-"));
  return imagesHTML(attachments) + (screenshots.length ? `<details class="computer-screenshots"><summary>电脑截图 · ${screenshots.length} 张</summary>${imagesHTML(screenshots)}</details>` : "");
}
function imagesHTML(images) {
  return `<div class="message-images">${(images || [])
    .filter(
      (a) =>
        /^image\/(png|jpeg|gif|webp)$/.test(a.mime) &&
        /^[A-Za-z0-9+/=\s]+$/.test(a.data),
    )
    .map(
      (a) =>
        `<img src="data:${esc(a.mime)};base64,${esc(a.data)}" alt="${esc(a.name)}" loading="lazy">`,
    )
    .join("")}</div>`;
}
function renderMessages(changed) {
  const c = state.conversation,
    stick = changed || nearBottom();
  const messages = c?.messages || [],
    sig = JSON.stringify([messages, c?.pending, c?.busy]);
  if (sig !== messageSignature) {
    messageSignature = sig;
    const existing = new Map(
      [...$("messages").children].map((el) => [el.dataset.id, el]),
    );
    for (const [index, m] of messages.entries()) {
      let el = existing.get(m.id);
      if (!el) {
        el = document.createElement("article");
        el.dataset.id = m.id;
      }
      existing.delete(m.id);
      const html = messageHTML(m);
      if (el._html !== html) {
        const toolsOpen = el.querySelector("details")?.open;
        el.className =
          "message " +
          (m.role === "user"
            ? "user"
            : m.role === "error"
              ? "error"
              : "assistant");
        el.innerHTML = html;
        el._html = html;
        if (toolsOpen && el.querySelector("details"))
          el.querySelector("details").open = true;
      }
      const atIndex = $("messages").children[index];
      if (atIndex !== el) $("messages").insertBefore(el, atIndex || null);
    }
    for (const el of existing.values()) el.remove();
    $("pending").innerHTML = (c?.pending || [])
      .filter((p) => !messages.some((m) => m.id === p.id))
      .map(
        (p) =>
          `<div class="pending-message"><small>等待处理</small>${esc(p.text)}${imagesHTML(p.attachments)}</div>`,
      )
      .join("");
    $("typing").hidden = !c?.busy;
    if (stick) requestAnimationFrame(bottom);
  }
}
function renderComposer() {
  const c = state.conversation,
    b = currentBot(),
    disabled = !c || c.closed;
  $("draft").disabled = disabled;
  $("draft").placeholder = !c
    ? "添加 AI 伙伴，开始第一段对话。"
    : c.closed
      ? "这段会话为只读，可新建对话继续。"
      : c.busy
        ? "继续补充想法，或发送追加指令…"
        : "写下你的想法，让我们一起完成。";
  $("attach").disabled = disabled;
  $("model").disabled = !b || !!c?.room_id;
  $("model-name").textContent = currentRoom()
    ? "团队协作"
    : b?.model || b?.engine || "默认模型";
  $("send-mode").hidden = !c?.busy || !!c?.room_id;
  const hasDraft =
    !!$("draft").value.trim() || !!imageDrafts.get(conversationKey)?.length;
  $("send").disabled = disabled || !hasDraft || sending.has(conversationKey);
  $("send").hidden = !!c?.busy && !hasDraft;
  $("stop").hidden = !c?.busy;
  $("attachments").innerHTML = (imageDrafts.get(conversationKey) || [])
    .map(
      (a, i) =>
        `<div class="attachment"><img src="data:${esc(a.mime)};base64,${esc(a.data)}" alt="${esc(a.name)}"><button data-remove-image="${i}" aria-label="移除 ${esc(a.name)}">×</button></div>`,
    )
    .join("");
}
function renderApprovals() {
  const approvals = state.conversation?.approvals || [],
    sig = JSON.stringify(approvals);
  if (sig === approvalSignature) return;
  approvalSignature = sig;
  // Preserve partially typed answers when another approval arrives.
  const answers = new Map(
    [...$("approvals").querySelectorAll("input")].map((el) => [
      el.dataset.answer,
      el.value,
    ]),
  );
  $("approvals").innerHTML = approvals
    .map(
      ({ bot_id, request: r }) =>
        `<section class="approval" data-approval="${esc(r.id)}" data-bot="${esc(bot_id)}"><h4>${esc(botByID(bot_id)?.name || "伙伴")} · ${r.kind === "question" ? "需要你的回答" : "请求使用 " + (r.tool || "工具")}</h4>${r.question ? `<p>${esc(r.question)}</p>` : ""}${r.input ? `<pre>${esc(JSON.stringify(r.input, null, 2))}</pre>` : ""}${r.kind === "question" ? `<input aria-label="你的回答" data-answer="${esc(r.id)}" placeholder="${esc((r.options || []).join(" / ") || "输入回答")}" value="${esc(answers.get(r.id) || "")}">` : ""}<div class="approval-actions"><button class="secondary" data-decision="deny">拒绝</button><button class="primary" data-decision="${r.kind === "question" ? "answer" : "allow"}">${r.kind === "question" ? "提交回答" : "允许本次"}</button></div></section>`,
    )
    .join("");
}
function renderInspector() {
  const b = currentBot(),
    r = currentRoom(),
    c = state.conversation;
  $("inspector").hidden = !detailsOpen || !c;
  if (!detailsOpen || !c) return;
  if (!r && b && !workDirs.has(b.id)) {
    workDirs.set(b.id, "");
    bridge("WorkDir", b.id)
      .then((dir) => {
        workDirs.set(b.id, dir);
        if (currentBot()?.id === b.id) renderInspector();
      })
      .catch(() => workDirs.delete(b.id));
  }
  $("inspector").innerHTML =
    `<div class="section-label"><span>${r ? "空间详情" : "关于这位伙伴"}</span><button class="icon-button" data-hide-details aria-label="关闭详情">${icon("close")}</button></div>${r ? `<span class="avatar large">${icon("hash")}</span>` : avatar(b, true)}<h3>${esc(r?.name || b?.name)}</h3><p>${esc(r?.instructions || b?.persona || "随时准备好，和你一起把事情做好。")}</p><div class="section-label">${r ? "协作配置" : "运行配置"}</div><div class="detail-row"><span>${r ? "负责人" : "引擎"}</span><strong>${esc(r ? botByID(r.lead_id)?.name : b?.engine)}</strong></div><div class="detail-row"><span>${r ? "参与伙伴" : "模型"}</span><strong>${esc(r ? r.members.map((id) => botByID(id)?.name || id).join("、") : b?.model || "默认模型")}</strong></div><div class="detail-row"><span>消息</span><strong>${c.messages?.length || 0} 条</strong></div>${!r ? `<div class="detail-row"><span>工具权限</span><strong>${{ ask: "逐次询问", "auto-edit": "自动编辑", "full-auto": "完全自动" }[b?.approval_mode || "ask"] || "逐次询问"}</strong></div>${workDirs.get(b?.id) ? `<div class="detail-row"><span>工作目录</span><strong>${esc(workDirs.get(b.id))}</strong></div>` : ""}` : ""}<hr><button class="detail-action" data-edit>${icon("settings")}编辑${r ? "空间" : "伙伴"}</button><button class="detail-action" data-memories>${icon("memory")}对话记忆</button>${!r ? `<button class="detail-action" data-folder>${icon("folder")}打开工作目录</button><button class="detail-action" data-new-thread>${icon("compose")}新建对话</button>` : ""}`;
}

function field(label, name, value = "", options = {}) {
  const control = options.textarea
    ? `<textarea name="${name}" ${options.required ? "required" : ""}>${esc(value)}</textarea>`
    : options.select
      ? `<select name="${name}">${options.select
          .map((item) => {
            const [v, label] = Array.isArray(item) ? item : [item, item];
            return `<option value="${esc(v)}" ${v === value ? "selected" : ""}>${esc(label)}</option>`;
          })
          .join("")}</select>`
      : `<input name="${name}" value="${esc(value)}" ${options.required ? "required" : ""} ${options.max ? `maxlength="${options.max}"` : ""} placeholder="${esc(options.placeholder || "")}">`;
  return `<label class="form-field"><span>${esc(label)}</span>${control}</label>`;
}
function openDialog(title, body, onSubmit) {
  const generation = ++dialogGeneration;
  $("dialog-body").onclick = null;
  $("dialog-title").textContent = title;
  $("dialog-error").hidden = true;
  $("dialog-body").innerHTML = body;
  if (!$("dialog").open) $("dialog").showModal();
  const form = $("dialog-body").querySelector("form");
  if (form && onSubmit)
    form.onsubmit = async (e) => {
      e.preventDefault();
      const submit = form.querySelector("[type=submit]");
      if (submit.disabled) return;
      submit.disabled = true;
      $("dialog-error").hidden = true;
      try {
        await onSubmit(new FormData(form), form);
        if (dialogGeneration === generation) $("dialog").close();
      } catch (err) {
        if (dialogGeneration === generation) {
          $("dialog-error").textContent = err.message;
          $("dialog-error").hidden = false;
        }
      } finally {
        submit.disabled = false;
      }
    };
  requestAnimationFrame(() =>
    form?.querySelector("input,textarea,select")?.focus(),
  );
}
const dialogActions = (label) =>
  `<div class="dialog-actions"><button type="button" class="secondary" data-cancel>取消</button><button type="submit" class="primary">${label}</button></div>`;
function openBot(b) {
  const original = b
    ? structuredClone(b)
    : {
        engine:
          state.engines.find((e) => e.available && e.name !== "echo")?.name ||
          "echo",
        approval_mode: "ask",
      };
  const engines = [
    ...new Set([
      original.engine,
      ...state.engines.filter((e) => e.available).map((e) => e.name),
      "echo",
    ]),
  ];
  openDialog(
    b ? "编辑 AI 伙伴" : "认识一位新伙伴",
    `<form>${field("名字", "name", original.name, { required: true, max: 120, placeholder: "给这位伙伴起个名字" })}<div class="form-row">${field("引擎", "engine", original.engine, { select: engines })}${field("头像文字 / Emoji", "avatar", original.avatar, { max: 8, placeholder: "留空使用名字首字" })}</div>${field("模型", "model", original.model, { placeholder: "留空使用引擎默认模型" })}<datalist id="model-options"></datalist><div class="form-row">${field("思考强度", "effort", original.effort || "", { select: [["", "默认"], "low", "medium", "high", "xhigh", "max"] })}${field(
      "工具权限",
      "approval_mode",
      original.approval_mode || "ask",
      {
        select: [
          ["ask", "逐次询问"],
          ["auto-edit", "自动编辑"],
          ["full-auto", "完全自动"],
        ],
      },
    )}</div><p class="form-help">逐次询问会在操作前请求确认；自动编辑允许修改文件；完全自动允许工具操作。更改模型会为后续消息建立新会话。</p>${field("角色与工作方式", "persona", original.persona, { textarea: true })}${dialogActions(b ? "保存更改" : "添加伙伴")}</form>`,
    async (data) => {
      const bot = { ...original, ...Object.fromEntries(data) };
      const engine = state.engines.find((e) => e.name === bot.engine);
      const model = engine?.models?.find((m) => m.id === bot.model);
      if (
        model?.efforts?.length &&
        bot.effort &&
        !model.efforts.includes(bot.effort)
      )
        throw new Error("所选模型不支持该思考强度，请选择默认或支持的强度。");
      await mutate({ kind: "save-bot", bot });
    },
  );
  const form = $("dialog-body").querySelector("form");
  form.elements.model.setAttribute("list", "model-options");
  const updateModels = () => {
    $("model-options").innerHTML = (
      state.engines.find((e) => e.name === form.elements.engine.value)
        ?.models || []
    )
      .map((m) => `<option value="${esc(m.id)}">${esc(m.name)}</option>`)
      .join("");
  };
  form.elements.engine.onchange = () => {
    form.elements.model.value = "";
    form.elements.effort.value = "";
    updateModels();
  };
  updateModels();
}
function openThread() {
  const b = currentBot();
  if (!b) return;
  openDialog(
    "开启一段新对话",
    `<form><p class="form-help">与 ${esc(b.name)} 开始独立的上下文，原有对话会保留。</p>${field("对话名称", "title", "", { required: true, max: 120, placeholder: "这次想聊些什么？" })}${dialogActions("新建对话")}</form>`,
    (data) =>
      mutate({ kind: "create-thread", bot_id: b.id, text: data.get("title") }),
  );
}
function openRoom(r) {
  if (!state.bots?.length) {
    notify("先添加一位 AI 伙伴，再创建协作空间。");
    return;
  }
  const original = r ? structuredClone(r) : { members: [], reply_mode: "lead" };
  openDialog(
    r ? "编辑协作空间" : "让伙伴们一起协作",
    `<form>${field("空间名称", "name", original.name, { required: true, max: 120, placeholder: "例如：产品工作室" })}<span class="form-help">参与伙伴</span>${state.bots.map((b) => `<label class="check-label"><input type="checkbox" name="members" value="${esc(b.id)}" ${original.members.includes(b.id) ? "checked" : ""}>${esc(b.name)} <span class="form-help">${esc(b.engine)}</span></label>`).join("")}<div class="form-row">${field("负责人", "lead_id", original.lead_id || state.bots[0].id, { select: state.bots.map((b) => [b.id, b.name]) })}${field(
      "回复方式",
      "reply_mode",
      original.reply_mode,
      {
        select: [
          ["lead", "负责人回复"],
          ["auto", "自动分配"],
        ],
      },
    )}</div><p class="form-help">负责人必须加入参与伙伴。可在消息中 @ 伙伴；自动分配使用引擎设置中的决策模型。</p>${field("共享指令", "instructions", original.instructions, { textarea: true })}${field("工作目录", "work_dir", original.work_dir, { placeholder: "可选，使用本机绝对路径" })}${dialogActions(r ? "保存更改" : "创建空间")}</form>`,
    (data) =>
      mutate({
        kind: "save-room",
        room: {
          ...original,
          ...Object.fromEntries(data),
          members: data.getAll("members"),
        },
      }),
  );
}
async function openSettings() {
  const settings = await action({ kind: "settings" });
  const engines = [
    ...new Set([
      "claude",
      "codex",
      "pi",
      "grok",
      "openai",
      ...state.engines.map((e) => e.name),
    ]),
  ].filter((e) => e !== "echo");
  openDialog(
    "引擎设置",
    `<form>${field("选择引擎", "engine", engines[0], { select: engines })}<div id="engine-settings"></div><p class="form-help">使用环境变量名称配置 API 密钥。密钥本身不会保存在界面中。</p><button type="button" class="secondary" data-routing>配置群聊决策模型</button><button type="button" class="secondary" data-tools-home>工具与自动化</button><button type="button" class="secondary" data-computer-home>伙伴的电脑</button><button type="button" class="secondary" data-voice-home>语音与通话</button><button type="button" class="secondary" data-team-home>团队与分享</button><button type="button" class="secondary" data-remote-home>手机与远程访问</button><button type="button" class="secondary" data-update>检查更新</button>${dialogActions("保存设置")}</form>`,
    async (data) => {
      keep();
      await mutate({ kind: "save-settings", settings });
      notify("引擎设置已保存");
    },
  );
  const form = $("dialog-body").querySelector("form");
  let selected = engines[0];
  const keep = () => {
    const v = Object.fromEntries(new FormData(form));
    settings[selected] = {
      path: v.path || "",
      base_url: v.base_url || "",
      api_key_env: v.api_key_env || "",
      models: (v.models || "")
        .split(/[,\n]/)
        .map((s) => s.trim())
        .filter(Boolean),
    };
  };
  const load = () => {
    const s = settings[selected] || {};
    $("engine-settings").innerHTML =
      `${field("CLI 路径", "path", s.path, { placeholder: "留空自动从 PATH 查找" })}${field("API 地址", "base_url", s.base_url, { placeholder: "例如 https://api.openai.com/v1" })}${field("密钥环境变量", "api_key_env", s.api_key_env, { placeholder: "例如 OPENAI_API_KEY" })}${field("模型列表（逗号或换行分隔）", "models", (s.models || []).join("\n"), { textarea: true })}`;
  };
  form.elements.engine.onchange = () => {
    keep();
    selected = form.elements.engine.value;
    load();
  };
  load();
}
async function openRouting() {
  const r = await action({ kind: "routing" });
  openDialog(
    "群聊决策模型",
    `<form><p class="form-help">用于自动选择下一位回复的伙伴。</p>${field("API 地址", "base_url", r.base_url, { required: true })}${field("密钥环境变量", "api_key_env", r.api_key_env)}${field("模型 ID", "model", r.model)}${dialogActions("保存配置")}</form>`,
    (data) =>
      mutate({ kind: "save-routing", routing: Object.fromEntries(data) }),
  );
}
async function openMemories(
  scopeID = state.conversation?.room_id || state.conversation?.id,
) {
  if (!scopeID) return;
  const notes = (await action({ kind: "memories", scope: scopeID })) || [];
  openDialog(
    "值得记住的事",
    `<p class="form-help">这些笔记会作为当前对话的上下文，帮助伙伴更了解你。</p><div id="memory-list">${notes.map((n) => `<div class="memory-item"><p>${esc(n.text)}</p><div class="memory-actions"><button data-edit-memory="${esc(n.id)}">编辑</button><button data-delete-memory="${esc(n.id)}" class="danger">删除</button></div></div>`).join("") || '<p class="form-help">还没有记忆。可以记下偏好、背景或项目约定。</p>'}</div><form>${field("添加一条记忆", "text", "", { textarea: true, required: true })}${dialogActions("保存记忆")}</form>`,
    async (data) => {
      await mutate({
        kind: "save-memory",
        scope: scopeID,
        memory: { text: data.get("text") },
      });
      await openMemories(scopeID);
    },
  );
  $("memory-list").onclick = run(async (e) => {
    const del = e.target.closest("[data-delete-memory]"),
      edit = e.target.closest("[data-edit-memory]");
    if (del) {
      const id = del.dataset.deleteMemory;
      confirmAction(
        "删除这条记忆？",
        "删除后，这条笔记将不再进入后续对话上下文。",
        async () => {
          await mutate({ kind: "delete-memory", scope: scopeID, id });
          await openMemories(scopeID);
        },
      );
    }
    if (edit) {
      const note = notes.find((n) => n.id === edit.dataset.editMemory);
      openDialog(
        "编辑记忆",
        `<form>${field("记忆内容", "text", note.text, { textarea: true, required: true })}${dialogActions("保存更改")}</form>`,
        async (data) => {
          await mutate({
            kind: "save-memory",
            scope: scopeID,
            memory: { ...note, text: data.get("text") },
          });
          await openMemories(scopeID);
        },
      );
    }
  });
}
function confirmAction(title, description, fn) {
  openDialog(
    title,
    `<form><p class="form-help">${esc(description)}</p>${dialogActions("确认")}</form>`,
    fn,
  );
}
function openMenu() {
  const b = currentBot(),
    r = currentRoom(),
    c = state.conversation;
  if (!c) return;
  const buttons = r
    ? [
        ["edit", "编辑协作空间"],
        ["memory", "对话记忆"],
        ["delete", "删除协作空间"],
      ]
    : [
        ["edit", "编辑伙伴与模型"],
        ["new", "新建对话"],
        ["memory", "对话记忆"],
        ["pin", b?.pinned ? "取消置顶" : "置顶伙伴"],
        ["unread", "标记为未读"],
        ["clone", "复制伙伴"],
        ["folder", "打开工作目录"],
        ["cursor", "复制会话 ID"],
        ["hidden", b?.hidden ? "显示伙伴" : "隐藏伙伴"],
        ["closed", showClosed ? "收起已关闭对话" : "显示已关闭对话"],
        ...(c.closed
          ? []
          : [
              ["compact", "压缩当前上下文"],
              ["close", "关闭当前对话"],
            ]),
        ["delete", "删除伙伴"],
      ];
  openDialog(
    "对话与伙伴",
    `<div class="menu-list">${buttons.map(([key, label]) => `<button data-menu-action="${key}" class="${key === "delete" ? "danger" : ""}">${label}</button>`).join("")}</div>`,
  );
  $("dialog-body").onclick = run(async (e) => {
    const key = e.target.closest("[data-menu-action]")?.dataset.menuAction;
    if (!key) return;
    $("dialog-body").onclick = null;
    $("dialog").close();
    if (key === "edit") {
      r ? openRoom(r) : openBot(b);
      return;
    }
    if (key === "new") {
      openThread();
      return;
    }
    if (key === "memory") {
      await openMemories();
      return;
    }
    if (key === "folder") {
      await bridge("OpenWorkDir", b.id);
      return;
    }
    if (key === "cursor") {
      await bridge("Copy", currentMeta()?.cursor || b.cursor || "");
      notify("会话 ID 已复制");
      return;
    }
    if (key === "clone") {
      await mutate({ kind: "clone-bot", id: b.id });
      return;
    }
    if (key === "closed") {
      showClosed = !showClosed;
      renderNavigation();
      return;
    }
    if (["pin", "hidden", "unread"].includes(key)) {
      const bot = { ...b };
      if (key === "pin") bot.pinned = !bot.pinned;
      if (key === "hidden") bot.hidden = !bot.hidden;
      if (key === "unread") bot.unread = true;
      await mutate({ kind: "save-bot", bot });
      return;
    }
    if (key === "compact") {
      confirmAction(
        "压缩当前上下文？",
        "会话将使用摘要和最近消息继续，完整记录仍保留。",
        () => mutate({ kind: "compact", id: c.id }),
      );
      return;
    }
    if (key === "close") {
      confirmAction(
        "关闭这段对话？",
        "保留消息记录，关闭后不能继续发送消息。",
        () => mutate({ kind: "close-thread", id: c.id }),
      );
      return;
    }
    if (key === "delete")
      confirmAction(
        `删除${r ? "空间" : "伙伴"}「${r?.name || b.name}」？`,
        "此操作会删除相关对话记录，无法撤销。",
        () =>
          mutate({ kind: r ? "delete-room" : "delete-bot", id: r?.id || b.id }),
      );
  });
}
function openSearch() {
  openDialog(
    "搜索对话",
    `<form><div class="form-row" style="grid-template-columns:1fr auto"><input name="query" aria-label="搜索关键词" placeholder="搜索想法、问题或关键词…" required maxlength="512"><button class="primary" type="submit">搜索</button></div></form><div id="search-results"><p class="form-help">在所有伙伴、协作空间和已关闭对话中查找。</p></div>`,
  );
  const form = $("dialog-body").querySelector("form");
  let version = 0;
  form.onsubmit = run(async (e) => {
    e.preventDefault();
    const request = ++version;
    const generation = dialogGeneration;
    $("search-results").innerHTML = '<p class="form-help">正在查找…</p>';
    let results;
    try {
      results =
        (await action({ kind: "search", text: form.elements.query.value })) ||
        [];
    } catch (err) {
      if (generation === dialogGeneration)
        $("search-results").innerHTML =
          `<p class="form-error">${esc(err.message)}</p>`;
      return;
    }
    if (request !== version || generation !== dialogGeneration) return;
    $("search-results").innerHTML =
      results
        .map(
          (r, i) =>
            `<button class="search-result" data-result="${i}"><small>${esc(r.room_id ? state.rooms.find((x) => x.id === r.room_id)?.name : botByID(r.bot_id)?.name)} · ${time(r.message.at)}</small><p>${esc(r.message.text.slice(0, 300))}</p></button>`,
        )
        .join("") ||
      '<p class="form-help">没有找到相关消息，试试其他关键词。</p>';
    $("search-results").onclick = run(async (e) => {
      const el = e.target.closest("[data-result]");
      if (!el) return;
      const r = results[Number(el.dataset.result)];
      await mutate({
        kind: "select",
        id: r.room_id || r.thread_id || r.bot_id,
        scope: r.room_id ? "room" : "thread",
      });
      $("dialog").close();
      requestAnimationFrame(() => {
        const el = [...$("messages").children].find(
          (el) => el.dataset.id === r.message.id,
        );
        el?.scrollIntoView({ block: "center" });
        el?.classList.add("highlight");
      });
    });
  });
  requestAnimationFrame(() => form.elements.query.focus());
}

$("draft").addEventListener("input", () => {
  drafts.set(conversationKey, $("draft").value);
  resizeDraft();
  renderComposer();
});
async function send() {
  const c = state.conversation;
  if (!c || c.closed || sending.has(c.id)) return;
  const key = c.id,
    text = $("draft").value,
    attachments = imageDrafts.get(key) || [];
  if (!text.trim() && !attachments.length) return;
  sending.add(key);
  renderComposer();
  try {
    await action({
      kind: "send",
      ...scope(c),
      text,
      attachments,
      mode: $("send-mode").value,
    });
    // Preserve edits made while the service admitted this message.
    if (drafts.get(key) === text) {
      drafts.delete(key);
      if (conversationKey === key) {
        $("draft").value = "";
        resizeDraft();
      }
    }
    if (imageDrafts.get(key) === attachments) imageDrafts.delete(key);
    await refresh();
    if (conversationKey === key) {
      bottom();
      $("draft").focus();
    }
  } finally {
    sending.delete(key);
    renderComposer();
  }
}
$("draft").addEventListener("keydown", (e) => {
  if (e.key === "Enter" && !e.shiftKey && !e.isComposing && e.keyCode !== 229) {
    e.preventDefault();
    send().catch(showError);
  }
});
$("send").onclick = run(send);
$("stop").onclick = run(async () => {
  const c = state.conversation;
  if (c) await mutate({ kind: "stop", ...scope(c) });
});
$("attach").onclick = run(async () => {
  const key = conversationKey;
  const images = await bridge("PickImages");
  const existing = imageDrafts.get(key) || [];
  if (existing.length + images.length > 4)
    throw new Error("每条消息最多添加 4 张图片");
  imageDrafts.set(key, [...existing, ...images]);
  if (key === conversationKey) renderComposer();
});
$("attachments").onclick = (e) => {
  const el = e.target.closest("[data-remove-image]");
  if (!el) return;
  const images = [...(imageDrafts.get(conversationKey) || [])];
  images.splice(Number(el.dataset.removeImage), 1);
  imageDrafts.set(conversationKey, images);
  renderComposer();
};
$("bots").onclick = run(async (e) => {
  const el = e.target.closest("[data-select]");
  if (el) await mutate({ kind: "select", id: el.dataset.select });
});
$("rooms").onclick = run(async (e) => {
  const el = e.target.closest("[data-room]");
  if (el) await mutate({ kind: "select", id: el.dataset.room, scope: "room" });
});
$("new-bot").onclick = () => openBot();
$("new-room").onclick = () => openRoom();
$("new-thread").onclick = openThread;
$("model").onclick = () => {
  if (currentBot()) openBot(currentBot());
};
$("details").onclick = () => {
  detailsOpen = !detailsOpen;
  renderInspector();
};
const toolsUI = createToolsUI({ bridge, openDialog, field, dialogActions, getBots: () => state.bots || [], notify, run });
const remoteUI = createRemoteUI({ bridge, openDialog, field, dialogActions, getBots: () => state.bots || [], notify, run });
const teamUI = createTeamUI({ bridge, openDialog, field, dialogActions, notify, run, refresh, getRooms: () => state.rooms || [] });
const voiceUI = createVoiceUI({
  bridge, openDialog, field, dialogActions, notify, run,
  getConversation: () => state.conversation,
  sendText: async (text, captured) => {
    if (!captured || captured.closed) throw new Error("请选择可发送消息的对话");
    await mutate({ kind: "send", ...scope(captured), text, mode: "queue" });
  },
  setDraft: (text) => {
    if (!state.conversation || state.conversation.closed) return;
    $("draft").value = text;
    drafts.set(conversationKey, text);
    resizeDraft();
    renderComposer();
  },
});
const computerUI = createComputerUI({ bridge, openDialog, field, dialogActions, getBots: () => state.bots || [], getConversation: () => state.conversation, notify, run });
window.addEventListener("pagehide", () => { voiceUI.shutdown(); computerUI.shutdown(); });
$("computer-panel").onclick = run(() => computerUI.home());
$("dictate").onclick = run(() => voiceUI.dictate());
$("voice-call").onclick = run(() => voiceUI.toggleCall());
$("voice-stop").onclick = run(() => voiceUI.stop());
if (!(globalThis.SpeechRecognition || globalThis.webkitSpeechRecognition)) {
  for (const id of ["dictate", "voice-call"]) {
    $(id).disabled = true;
    $(id).title = "此 WebView 不支持语音识别，请使用系统键盘听写";
  }
}
$("settings").onclick = run(openSettings);
$("search").onclick = openSearch;
$("show-hidden").onclick = run(async () => {
  showHidden = !showHidden;
  await mutate({ kind: "visibility", show_hidden: showHidden });
});
$("close-dialog").onclick = () => $("dialog").close();
$("dialog").addEventListener("close", () => {
  if (!$("dialog").open) {
    dialogGeneration++;
    $("dialog-body").onclick = null;
  }
});
$("dialog").addEventListener("click", (e) => {
  if (e.target.closest("[data-cancel]")) $("dialog").close();
  if (e.target.closest("[data-routing]")) openRouting().catch(showError);
  if (e.target.closest("[data-tools-home]")) toolsUI.home().catch(showError);
  if (e.target.closest("[data-remote-home]")) remoteUI.home().catch(showError);
  if (e.target.closest("[data-team-home]")) teamUI.home().catch(showError);
  if (e.target.closest("[data-voice-home]")) voiceUI.home().catch(showError);
  if (e.target.closest("[data-computer-home]")) computerUI.home().catch(showError);
  if (e.target.closest("[data-update]")) bridge("CheckForUpdates").catch(showError);
});
$("welcome").onclick = (e) => {
  const el = e.target.closest("[data-prompt]");
  if (!el) return;
  if (!state.conversation) {
    openBot();
    return;
  }
  $("draft").value = el.dataset.prompt;
  drafts.set(conversationKey, el.dataset.prompt);
  resizeDraft();
  renderComposer();
  $("draft").focus();
};
$("conversation-header").onclick = (e) => {
  if (e.target.closest("[data-menu]")) openMenu();
};
$("status").onclick = run(async (e) => {
  if (e.target.closest("[data-dismiss]"))
    await mutate({ kind: "dismiss-status" });
});
$("inspector").onclick = run(async (e) => {
  if (e.target.closest("[data-hide-details]")) {
    detailsOpen = false;
    renderInspector();
  }
  if (e.target.closest("[data-edit]"))
    currentRoom() ? openRoom(currentRoom()) : openBot(currentBot());
  if (e.target.closest("[data-memories]")) await openMemories();
  if (e.target.closest("[data-folder]"))
    await bridge("OpenWorkDir", currentBot().id);
  if (e.target.closest("[data-new-thread]")) openThread();
});
$("messages").onclick = run(async (e) => {
  const speak = e.target.closest("[data-speak-message]");
  if (speak) {
    const message = state.conversation?.messages.find((m) => m.id === speak.dataset.speakMessage);
    if (message) await voiceUI.speak(message.text);
    return;
  }
  const link = e.target.closest("a");
  if (link) {
    e.preventDefault();
    await bridge("OpenLink", link.getAttribute("href"));
    return;
  }
  const copy = e.target.closest("[data-copy-message]"),
    code = e.target.closest("[data-copy-code]");
  if (copy) {
    const m = state.conversation.messages.find(
      (m) => m.id === copy.dataset.copyMessage,
    );
    await bridge("Copy", m.text);
    notify("消息已复制");
  }
  if (code) {
    await bridge(
      "Copy",
      code.closest(".code-block").querySelector("code").textContent,
    );
    notify("代码已复制");
  }
});
$("approvals").onclick = run(async (e) => {
  const button = e.target.closest("[data-decision]");
  if (!button) return;
  const card = button.closest("[data-approval]"),
    answer = card.querySelector("input")?.value || "";
  if (button.dataset.decision === "answer" && !answer.trim())
    throw new Error("请先填写回答");
  button.disabled = true;
  try {
    await mutate({
      kind: "approve",
      id: card.dataset.approval,
      bot_id: card.dataset.bot,
      decision: { outcome: button.dataset.decision, answer },
    });
  } finally {
    button.disabled = false;
  }
});
$("message-scroll").addEventListener(
  "scroll",
  () => ($("jump-bottom").hidden = nearBottom()),
);
$("jump-bottom").onclick = bottom;
let theme;
try {
  theme = localStorage.getItem("bqbot-theme");
} catch {}
const media = matchMedia("(prefers-color-scheme: dark)");
function applyTheme() {
  const dark = theme ? theme === "dark" : media.matches;
  document.documentElement.dataset.theme = dark ? "dark" : "light";
  $("theme").innerHTML = icon(dark ? "moon" : "sun");
  $("theme").title = dark ? "切换为浅色" : "切换为深色";
}
$("theme").onclick = () => {
  theme = document.documentElement.dataset.theme === "dark" ? "light" : "dark";
  try {
    localStorage.setItem("bqbot-theme", theme);
  } catch {}
  applyTheme();
};
media.addEventListener("change", applyTheme);
applyTheme();
document.addEventListener("keydown", (e) => {
  if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
    e.preventDefault();
    openSearch();
  }
  if (
    (e.metaKey || e.ctrlKey) &&
    e.key.toLowerCase() === "n" &&
    !$("dialog").open
  ) {
    e.preventDefault();
    openThread();
  }
});
// Load recovery state after registering the invalidation listener. Refreshes
// are coalesced and serialized so a late response cannot replace newer state.
if (window.mygo) window.mygo.on("desktop:changed", scheduleRefresh);
refresh().catch((err) => {
  render();
  $("status").hidden = false;
  $("status").textContent = err.message;
});
