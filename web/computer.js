import { escapeHTML as esc } from "./markdown.js";

// Preview polling lasts only while this dialog remains open. No credentials or
// remote desktop URL are placed in an image element or iframe.
export function createComputerUI({ bridge, openDialog, field, dialogActions, getBots, getConversation, notify, run }) {
  const call = (kind, extra = {}) => bridge("Computer", { kind, ...extra });
  const body = () => document.getElementById("dialog-body");
  let generation = 0;
  function shutdown() { generation++; }
  async function home() {
    shutdown();
    const items = await call("computers") || [];
    const bots = getBots();
    openDialog("伙伴的电脑", `<p class="form-help">连接已有的 Boat 云桌面或 Cua 桌面。电脑默认关闭，宿主机访问需单独授权。</p><div class="tool-list">${bots.map((b) => { const c = items.find((v) => v.bot_id === b.id); return `<div class="tool-item"><div><strong>${esc(b.name)}</strong><p class="form-help">${c ? `${esc(c.provider)} · ${c.enabled ? "启用" : "关闭"}` : "尚未配置"}</p></div><button type="button" class="secondary" data-config="${esc(b.id)}">配置</button>${c?.enabled ? `<button type="button" class="secondary" data-preview="${esc(b.id)}">打开电脑</button>` : ""}</div>`; }).join("") || '<p class="form-help">先创建一位伙伴。</p>'}</div>`);
    body().onclick = run(async (e) => {
      const config = e.target.closest("[data-config]"), preview = e.target.closest("[data-preview]");
      if (config) await edit(items.find((v) => v.bot_id === config.dataset.config) || { bot_id: config.dataset.config });
      if (preview) await panel(items.find((v) => v.bot_id === preview.dataset.preview));
    });
  }
  async function edit(c) {
    shutdown();
    openDialog("电脑连接", `<form>${field("类型", "provider", c.provider || "boat", { select: [["boat", "Boat 云桌面"], ["vm", "本地 / 远程 VM（Cua MCP）"], ["host", "这台电脑（Cua MCP）"]] })}${field("API / MCP 地址", "url", c.url || "https://boat.dev/api/v1", { required: true })}${field("凭据环境变量名", "token_env", c.token_env || "")}${field("Boat Sandbox ID", "sandbox_id", c.sandbox_id || "")}<label class="check-label"><input type="checkbox" name="enabled" ${c.enabled ? "checked" : ""}>启用此电脑</label><label class="check-label"><input type="checkbox" name="host_consent" ${c.host_consent ? "checked" : ""}>允许访问宿主机屏幕及输入设备</label><p class="form-help">Boat 使用已有 Sandbox，需安装 scrot 和 xdotool。VM / 宿主使用 Cua 的 HTTP MCP 地址；宿主地址只接受本机回环。Bot 操作仍经过每轮工具审批。</p>${dialogActions("保存电脑")}</form>`, async (data) => {
      await call("save-computer", { config: { bot_id: c.bot_id, provider: data.get("provider"), url: data.get("url"), token_env: data.get("token_env"), sandbox_id: data.get("sandbox_id"), enabled: data.has("enabled"), host_consent: data.has("host_consent") } });
      notify("电脑配置已保存");
    });
  }
  async function panel(c) {
    shutdown(); const current = generation;
    const bot = getBots().find((b) => b.id === c.bot_id);
    openDialog(`${bot?.name || "伙伴"}的电脑`, `<p class="form-help" data-computer-status>正在获取画面…</p><img data-computer-frame alt="电脑实时截图" style="width:100%;max-height:55vh;object-fit:contain;border-radius:8px" hidden><label class="check-label"><input type="checkbox" data-control>允许当前面板发送鼠标和键盘输入</label><div class="tool-buttons"><button type="button" class="secondary" data-shot>刷新</button><button type="button" class="secondary" data-capture>发送截图到当前对话</button><button type="button" class="secondary" data-key="Return">Enter</button><button type="button" class="secondary" data-key="Escape">Esc</button><button type="button" class="secondary" data-scroll="up">向上滚动</button><button type="button" class="secondary" data-scroll="down">向下滚动</button></div><form data-type-form>${field("输入文字", "text", "")}${dialogActions("输入到电脑")}</form>`);
    const root = body(), image = root.querySelector("[data-computer-frame]"), status = root.querySelector("[data-computer-status]");
    let pending = false, frame = null;
    const alive = () => current === generation && image.isConnected && root.closest("dialog")?.open;
    const control = async (action) => {
      if (!root.querySelector("[data-control]").checked) throw new Error("先勾选允许当前面板输入");
      await call("action", { bot_id: c.bot_id, action, confirmed: true });
    };
    const refresh = async () => {
      if (!alive() || pending) return;
      pending = true;
      try {
        const next = await call("action", { bot_id: c.bot_id, action: { kind: "screenshot" } });
        if (!alive()) return;
        frame = next;
        if (frame.image && ["image/png", "image/jpeg"].includes(frame.image.mime)) { image.src = `data:${frame.image.mime};base64,${frame.image.data}`; image.hidden = false; }
        status.textContent = "画面每三秒刷新；点击画面可定位鼠标。";
      } catch (e) { if (alive()) status.textContent = e.message; }
      finally { pending = false; }
    };
    const poll = async () => { await refresh(); if (alive()) setTimeout(poll, 3000); };
    root.onclick = run(async (e) => {
      if (e.target.closest("[data-shot]")) await refresh();
      if (e.target.closest("[data-capture]")) {
        const conversation = getConversation?.();
        if (!conversation || conversation.bot_id !== c.bot_id || conversation.room_id || conversation.closed) throw new Error("先选择此伙伴的开放私人对话");
        await call("capture-to-chat", { bot_id: c.bot_id, thread_id: conversation.id });
        notify("截图已发送到对话");
      }
      const key = e.target.closest("[data-key]"), scroll = e.target.closest("[data-scroll]");
      if (key) await control({ kind: "key", key: key.dataset.key });
      if (scroll) await control({ kind: "scroll", direction: scroll.dataset.scroll });
      if (e.target === image && frame) {
        const rect = image.getBoundingClientRect(), scale = Math.min(rect.width / frame.width, rect.height / frame.height);
        const left = rect.left + (rect.width - frame.width * scale) / 2, top = rect.top + (rect.height - frame.height * scale) / 2;
        const x = Math.floor((e.clientX - left) / scale), y = Math.floor((e.clientY - top) / scale);
        if (x >= 0 && y >= 0 && x < frame.width && y < frame.height) await control({ kind: "click", x, y });
      }
    });
    root.querySelector("form").onsubmit = run(async (e) => { e.preventDefault(); const input = e.target.elements.text; await control({ kind: "type", text: input.value }); input.value = ""; await refresh(); });
    await poll();
  }
  return { home, shutdown };
}
