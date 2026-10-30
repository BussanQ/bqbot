import { escapeHTML as esc } from "./markdown.js";

export function createRemoteUI({ bridge, openDialog, field, dialogActions, getBots, run, notify }) {
  const call = (kind, extra = {}) => bridge("Remote", { kind, ...extra });
  async function home() {
    const state = await call("state");
    openDialog("手机与远程访问", `<form><p class="form-help">通过 Tailscale Serve 或 HTTPS 反向代理转发到 127.0.0.1:8801。设备只能访问你选择的伙伴；审批仍在桌面处理。关闭应用后配对失效。</p>${field("HTTPS 访问地址", "origin", state.origin || "", { placeholder: "https://your-device.example.ts.net", required: !state.enabled })}<p>${state.enabled ? `已开启 · ${state.devices} 台设备` : "尚未开启"}</p>${state.enabled ? '<button type="button" class="secondary" data-pair>配对新设备</button><button type="button" class="secondary" data-revoke>撤销所有设备</button>' : ""}${dialogActions(state.enabled ? "关闭远程访问" : "开启远程访问")}</form>`, async (data) => {
      if (state.enabled) await call("disable");
      else await call("enable", { origin: data.get("origin").trim().replace(/\/$/, "") });
      notify(state.enabled ? "远程访问已关闭" : "远程访问已开启");
    });
    document.querySelector("[data-pair]")?.addEventListener("click", run(async () => {
      openDialog("授权并配对设备", `<form><p class="form-help">该设备可以读取所选伙伴的聊天记录并发送消息，沿用伙伴的工具权限。配对码五分钟内有效，仅可使用一次。</p>${getBots().map((b) => `<label class="check-label"><input type="checkbox" name="bot_ids" value="${esc(b.id)}">${esc(b.name)}</label>`).join("")}${dialogActions("生成配对码")}</form>`, async (data) => {
        const result = await call("pair", { bot_ids: data.getAll("bot_ids") });
        openDialog("在设备上输入配对码", `<p>打开 ${esc(state.origin)}</p><p class="pair-code">${esc(result.code)}</p><p class="form-help">五分钟后失效。连续输错五次会使当前配对码失效。</p>`);
      });
    }));
    document.querySelector("[data-revoke]")?.addEventListener("click", run(async () => { await call("revoke"); notify("已撤销所有设备和配对码"); await home(); }));
  }
  return { home };
}
