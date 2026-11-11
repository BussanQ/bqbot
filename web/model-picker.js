import { agentName, hasTools, reasoningLevels } from "./agents.js";

export function modelOptions(models, selected, defaultID) {
  const options = [["", defaultID ? `沿用 Agent 默认（${defaultID}）` : "沿用 Agent 默认"]];
  const ids = new Set([""]);
  for (const m of models || []) {
    if (!m.id || ids.has(m.id)) continue;
    ids.add(m.id);
    options.push([m.id, m.name && m.name !== m.id ? `${m.name} · ${m.id}` : m.id]);
  }
  if (selected && !ids.has(selected)) options.push([selected, selected]);
  return options;
}

const effortNames = { none: "无", off: "关闭", minimal: "最低", low: "低", medium: "中", high: "高", xhigh: "极高", max: "最大", ultra: "超高" };

export function createModelPicker({ getBot, getKey, getEngines, read, save, updated }) {
  const button = document.getElementById("model"), panel = document.getElementById("model-picker");
  const select = panel.querySelector("[name=model]"), effort = panel.querySelector("[name=effort]");
  const policy = panel.querySelector("[name=approval_mode]"), reload = panel.querySelector("[data-reload]");
  const note = panel.querySelector("[role=status]");
  const cache = new Map();
  let owner, signature, pending = null, error = "";
  function load(engine, force = false) {
    const previous = cache.get(engine);
    if (previous?.loading || (!force && previous)) return;
    const entry = { ...previous, loading: true };
    cache.set(engine, entry);
    Promise.resolve().then(() => read(engine)).then((list) => {
      if (cache.get(engine) !== entry) return;
      if (list.notice && previous?.source === "agent") entry.notice = "刷新失败，继续显示上次读取的模型列表。";
      else Object.assign(entry, { notice: "" }, list);
    }).catch(() => { entry.notice = "模型列表读取失败，可以重试。"; }).finally(() => {
      if (cache.get(engine) !== entry) return;
      entry.loading = false; updated();
    });
  }
  function close(restoreFocus = false) {
    owner = null; signature = null; error = ""; panel.hidden = true;
    button.setAttribute("aria-expanded", "false");
    if (restoreFocus) button.focus();
  }
  function position() {
    if (panel.hidden) return;
    const r = button.getBoundingClientRect();
    panel.style.left = Math.max(8, Math.min(innerWidth - panel.offsetWidth - 8, r.right - panel.offsetWidth)) + "px";
    panel.style.top = Math.max(8, r.top - panel.offsetHeight - 8) + "px";
  }
  function entryFor(bot) {
    const seed = getEngines().find((e) => e.name === bot.engine);
    return { ...seed, ...cache.get(bot.engine), name: bot.engine };
  }
  function levels(bot, id) {
    const entry = entryFor(bot);
    return reasoningLevels(entry, entry.aliases?.[id] || id || entry.default || "");
  }
  function render(bot, canEdit, isRoom) {
    if (owner && (!canEdit || owner.id !== bot?.id || owner.engine !== bot?.engine || owner.key !== getKey())) close();
    if (pending && bot && pending.id === bot.id && pending.engine === bot.engine) bot = { ...bot, ...pending };
    const entry = bot && entryFor(bot);
    if (bot && canEdit && !cache.has(bot.engine)) load(bot.engine);
    const title = isRoom ? "团队协作" : entry?.aliases?.[bot?.model] || bot?.model || entry?.default || (bot?.engine === "echo" ? "Echo" : "Agent 默认模型");
    button.querySelector("#model-name").textContent = title;
    button.title = isRoom ? title : [agentName(bot?.engine), title, bot?.effort && `推理强度：${effortNames[bot.effort] || bot.effort}`].filter(Boolean).join(" · ");
    button.disabled = !canEdit || bot?.engine === "echo";
    if (!owner) return;
    const state = JSON.stringify([bot.model, bot.effort, bot.approval_mode, entry, pending, error]);
    if (state === signature) return;
    signature = state;
    select.replaceChildren(...modelOptions(entry.models, bot.model, entry.default).map(([v, n]) => new Option(n, v)));
    select.value = bot.model || "";
    const choices = [...levels(bot, select.value)];
    if (bot.effort && !choices.includes(bot.effort)) choices.push(bot.effort);
    const defaultLevel = (!bot.model || bot.model === entry.default) && entry.default_effort || entry.models?.find((m) => m.id === (bot.model || entry.default))?.default_effort;
    effort.replaceChildren(new Option(defaultLevel ? `沿用 Agent 默认（${effortNames[defaultLevel] || defaultLevel}）` : "沿用 Agent 默认", ""), ...choices.map((v) => new Option(effortNames[v] || v, v)));
    effort.value = bot.effort || ""; effort.disabled = pending || !choices.length;
    if (!choices.length) effort.options[0].text = "此模型未提供推理档位";
    policy.closest("label").hidden = !hasTools(bot.engine);
    policy.value = bot.approval_mode || "ask";
    select.disabled = policy.disabled = pending;
    reload.disabled = pending || !!entry.loading;
    note.textContent = error || (pending ? "正在保存…" : entry.loading ? "正在读取模型…" : entry.notice || "适用于此伙伴的所有对话，从下一轮生效。");
    position();
  }
  button.onclick = () => {
    if (owner) return close(true);
    const bot = getBot();
    if (!bot || button.disabled) return;
    owner = { id: bot.id, engine: bot.engine, key: getKey() };
    panel.hidden = false; button.setAttribute("aria-expanded", "true");
    updated(); select.focus();
  };
  panel.onchange = async (event) => {
    const bot = getBot(), scope = owner;
    if (pending || !bot || bot.id !== scope?.id || bot.engine !== scope.engine) return;
    const model = select.value;
    const level = event.target === select && !levels(bot, model).includes(effort.value) ? "" : effort.value;
    const options = { engine: bot.engine, model, effort: level, approval_mode: policy.value };
    pending = { id: bot.id, ...options }; error = ""; render(bot, true, false);
    try { await save(bot.id, options); }
    catch (e) { if (owner === scope) error = e.message || String(e); }
    finally { pending = null; signature = null; updated(); }
  };
  reload.onclick = () => { if (owner) {error = ""; load(owner.engine, true); updated();} };
  document.addEventListener("pointerdown", (e) => { if (owner && !panel.contains(e.target) && !button.contains(e.target)) close(); });
  document.addEventListener("focusin", (e) => { if (owner && !panel.contains(e.target) && e.target !== button) close(); });
  document.addEventListener("keydown", (e) => {
    if (owner && e.key === "Escape") { e.preventDefault(); close(true); }
  });
  window.addEventListener("resize", position);
  return { render, close, load, clear: () => cache.clear(), entries: () => getEngines().map((e) => ({ ...e, ...cache.get(e.name) })) };
}
