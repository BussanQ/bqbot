// Agent availability, models and reasoning levels come from the local service.
import { escapeHTML as esc } from "./markdown.js";

const providers = {
  bqbot: { name: "bqbot", mark: "bq" },
  claude: { name: "Claude Code", mark: "✳" },
  codex: { name: "Codex", mark: ">_" },
  pi: { name: "Pi", mark: "π" },
  grok: { name: "Grok", mark: "G" },
  openai: { name: "OpenAI 兼容 API", mark: "API" },
  echo: { name: "Echo", mark: "↳" },
};
export const agentName = (id) => providers[id]?.name || id;
export const hasTools = (id) => id !== "openai" && id !== "echo";

// Only text can be steered, and existing queued work keeps its FIFO order.
export function canSteer(c, engine, attachments) {
  if (!c?.busy || c.closed || c.room_id || c.answering || c.queued || attachments?.length) return false;
  // Older local services omit the capability flag.
  return engine?.steering ?? ["codex", "pi"].includes(engine?.name);
}

function agentRow(engine, icon) {
  const id = engine.name;
  const local = hasTools(id) && id !== "bqbot";
  const description = local
    ? engine.version || `本机命令 · ${id}`
    : id === "bqbot" ? "通过模型服务使用伙伴、记忆和工作区工具" : id === "echo" ? "在本机回显消息" : "使用服务地址和模型 ID";
  const status = local
    ? (engine.available ? "已检测" : "需配置")
    : id === "bqbot" ? "内置" : id === "echo" ? "离线" : "API";
  return `<div class="agent-choice">
    <button type="button" class="agent-choice-main" ${engine.available ? `data-launch-agent="${esc(id)}"` : `data-setup-agent="${esc(id)}"`}>
      <span class="agent-mark" aria-hidden="true">${esc(providers[id]?.mark || id.slice(0, 1))}</span>
      <span class="agent-choice-label"><strong>${esc(agentName(id))}</strong><small title="${esc(description)}">${esc(description)}</small></span>
      <span class="agent-availability ${engine.available ? "" : "needs-setup"}">${status}</span>
    </button>
    ${id !== "echo" && engine.available ? `<button type="button" class="icon-button agent-setup" data-setup-agent="${esc(id)}" aria-label="配置 ${esc(agentName(id))}" title="配置 ${esc(agentName(id))}">${icon("settings")}</button>` : ""}
  </div>`;
}

export function renderAgentDirectory(container, state, icon) {
  const catalog = new Map((state.engines || []).map((e) => [e.name, e]));
  const engines = [...new Set([...Object.keys(providers), ...catalog.keys()])].map((name) => catalog.get(name)).filter(Boolean);
  const html = `<div class="agent-list-head"><span>Agent 接入</span>
    <button type="button" class="agent-recheck" data-probe-engines ${state.probing ? "disabled" : ""}>${icon("refresh")}${state.probing ? "检测中…" : "重新检测"}</button></div>
    ${state.probe_error ? `<p class="form-error" role="alert">无法获取 Agent 列表：${esc(state.probe_error)}</p>` : ""}
    ${engines.length ? `<div class="agent-choices">${engines.map((engine) => agentRow(engine, icon)).join("")}</div>` :
      !state.probe_error ? `<p class="form-help" role="status">${state.probing ? "正在检测本机 Agent…" : "尚未获取 Agent 列表。"}</p>` : ""}`;
  if (container._directoryHTML === html) return;
  const focused = document.activeElement;
  const focusKey = container.contains(focused) && focused.closest("button")?.dataset;
  container.innerHTML = html;
  container._directoryHTML = html;
  if (focusKey) {
    const [key] = Object.keys(focusKey);
    [...container.querySelectorAll("button")].find((b) => b.dataset[key] === focusKey[key])?.focus({ preventScroll: true });
  }
}

// Keep the inputs shared by creation, profile editing and the composer's Agent picker.
export function chatFields(field, bot) {
  return `${field("Agent", "engine", bot.engine, { select: [[bot.engine, agentName(bot.engine)]] })}
    <div data-model-field>${field("模型", "model", bot.model, { placeholder: "筛选或输入模型 ID，留空使用默认", max: 256 })}
      <div class="model-choices" role="group" aria-label="建议模型"></div>
      <p class="form-help" data-custom-model hidden>自定义模型的可用性和思考强度由提供方决定。</p></div>
    <div class="form-row agent-policy">
      ${field("思考强度", "effort", "", { select: [] })}
      ${field("工具权限", "approval_mode", bot.approval_mode || "ask", { select: [
        ["ask", "逐次询问"], ["auto-edit", "自动编辑"], ["full-auto", "完全自动"],
      ] })}
    </div><p class="form-help" data-permission-help></p>`;
}

export function reasoningLevels(engine, model) {
  if (!engine || engine.name === "echo") return [];
  const models = engine.models || [];
  if (!model) return [...new Set(models.flatMap((m) => m.efforts || []))];
  const match = models.find((m) => m.id === model);
  // The service accepts these values for custom IDs; the provider decides
  // which of them the actual model supports at runtime.
  return match ? match.efforts || [] : ["off", "minimal", "low", "medium", "high", "xhigh", "max"];
}

export function bindChatFields(form, bot, getEngines) {
  const { engine, model, effort, approval_mode: permission } = form.elements;
  const choices = form.querySelector(".model-choices");
  function update(search, level) {
    const entry = getEngines().find((e) => e.name === engine.value);
    const models = entry?.models || [];
    const query = search ? model.value.trim().toLowerCase() : "";
    const options = [{ id: "", name: "引擎默认" }, ...models.filter((m) => m.id.toLowerCase().includes(query))];
    choices.innerHTML = options.map((m) =>
      `<button type="button" data-model-id="${esc(m.id)}" aria-pressed="${m.id === model.value}">${esc(m.name || m.id)}</button>`).join("");
    form.querySelector("[data-model-field]").hidden = engine.value === "echo";
    const custom = entry && model.value !== "" && !models.some((m) => m.id === model.value);
    form.querySelector("[data-custom-model]").hidden = !custom;
    let levels = reasoningLevels(entry, model.value);
    // Preserve a saved choice while the initial catalog request is in flight.
    if (!entry && level && !levels.includes(level)) levels = [...levels, level];
    effort.replaceChildren(new Option("引擎默认", ""), ...levels.map((v) => new Option(v, v)));
    effort.value = levels.includes(level) ? level : "";
    effort.closest("label").hidden = !levels.length;
    permission.closest("label").hidden = !hasTools(engine.value);
    showPermissionHelp();
  }
  function showPermissionHelp() {
    const help = form.querySelector("[data-permission-help]");
    help.hidden = !hasTools(engine.value);
    help.textContent = {
      ask: "工具请求由你逐次确认。",
      "auto-edit": "常规文件编辑自动批准，其他工具仍需确认。",
      "full-auto": "自动批准工具请求。Agent 提问时仍会等待你的回答。",
    }[permission.value];
  }
  permission.onchange = showPermissionHelp;
  let previousEngine = bot.engine;
  engine.onchange = () => {
    if (!bot.id && form.elements.name?.value === agentName(previousEngine)) form.elements.name.value = agentName(engine.value);
    previousEngine = engine.value;
    model.value = "";
    permission.value = "ask";
    update(false, "");
  };
  model.oninput = () => update(true, effort.value);
  choices.onclick = (e) => {
    const option = e.target.closest("[data-model-id]");
    if (!option) return;
    model.value = option.dataset.modelId;
    update(false, effort.value);
    [...choices.children].find((b) => b.dataset.modelId === model.value)?.focus({ preventScroll: true });
  };
  let lastCatalog;
  function syncCatalog() {
    const engines = getEngines();
    const signature = JSON.stringify(engines);
    if (signature === lastCatalog) return;
    const level = lastCatalog === undefined ? bot.effort : effort.value;
    lastCatalog = signature;
    const selected = engine.value;
    const ids = new Set([bot.engine, selected, ...engines.filter((e) => e.available).map((e) => e.name)]);
    engine.replaceChildren(...[...ids].map((id) => new Option(agentName(id), id)));
    engine.value = selected;
    update(document.activeElement === model, level || "");
  }
  syncCatalog();
  return syncCatalog;
}
