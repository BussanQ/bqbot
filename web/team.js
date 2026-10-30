import { escapeHTML as esc } from "./markdown.js";

export function createTeamUI({ bridge, openDialog, field, dialogActions, notify, run, refresh, getRooms = () => [] }) {
  const call = (kind, extra = {}) => bridge("Team", { kind, ...extra });
  const body = () => document.getElementById("dialog-body");
  async function home() {
    const presets = await call("presets");
    openDialog("团队与分享", `<p class="form-help">导入会新建伙伴，权限设为逐次询问，自动任务保持暂停。</p><div class="tool-buttons"><button type="button" class="secondary" data-team-import>导入 Markdown</button><button type="button" class="secondary" data-team-export>导出团队或部门</button></div><h3>预设团队</h3>${presets.map((p, i) => `<button type="button" class="secondary" data-team-preset="${i}">${esc(p.name)}</button>`).join("")}`);
    body().onclick = run(async (event) => {
      if (event.target.closest("[data-team-import]")) return importTeam();
      if (event.target.closest("[data-team-export]")) return exportTeam();
      const preset = event.target.closest("[data-team-preset]");
      if (preset) await preview(presets[Number(preset.dataset.teamPreset)].markdown);
    });
  }
  function importTeam() {
    openDialog("导入团队", `<form><label class="form-field"><span>选择 Markdown 文件</span><input type="file" accept=".md,.markdown,text/markdown" data-team-file></label>${field("团队 Markdown", "markdown", "", { textarea: true, required: true })}${dialogActions("预览导入")}</form>`, async (data) => preview(data.get("markdown")));
    body().querySelector("[data-team-file]").onchange = run(async (event) => {
      const file = event.target.files[0];
      if (!file) return;
      if (file.size > 2 * 1024 * 1024) throw new Error("团队文件不能超过 2 MiB");
      body().querySelector('[name="markdown"]').value = await file.text();
    });
  }
  async function preview(markdown) {
    const d = await call("preview", { markdown });
    openDialog(`导入 ${d.name}`, `<form><p class="form-help">检查伙伴指令和任务内容后导入。所有伙伴都使用逐次询问权限，自动任务暂停。</p>${d.bots.map((b) => `<details class="tool-item"><summary>${esc(b.name)} · ${esc(b.engine)}</summary><pre>${esc(b.persona || "无预设指令")}</pre></details>`).join("")}<p class="form-help">${d.rooms?.length || 0} 个频道，${d.routines?.length || 0} 个自动任务。已有同名伙伴或频道时会拒绝导入。</p>${(d.routines || []).map((r) => `<details><summary>${esc(r.name)}</summary><pre>${esc(r.prompt)}</pre></details>`).join("")}${dialogActions("确认导入")}</form>`, async () => {
      await call("apply", { markdown });
      await refresh?.();
      notify("团队已导入，自动任务保持暂停");
    });
  }
  function exportTeam() {
    openDialog("导出团队", `<form>${field("团队名称", "name", "bqbot team", { required: true })}${field("导出范围", "room_id", "", { select: [["", "全部伙伴与频道"], ...getRooms().map((r) => [r.id, r.name])] })}<p class="form-help">不导出密钥、会话、工作目录、记忆或权限设置。人物指令、频道指令和任务文本会原样导出，请检查其中是否手写了敏感信息。</p>${dialogActions("生成 Markdown")}</form>`, async (data) => {
      const value = await call("export", { name: data.get("name"), room_id: data.get("room_id") });
      openDialog("团队 Markdown", `${field("分享内容", "markdown", value.markdown, { textarea: true })}<button type="button" class="secondary" data-team-download>保存 Markdown</button><button type="button" class="secondary" data-team-copy>复制内容</button>`);
      body().querySelector("[data-team-download]").onclick = run(async () => {
        if (await call("save-file", { markdown: value.markdown })) notify("团队 Markdown 已保存");
      });
      body().querySelector("[data-team-copy]").onclick = run(async () => {
        await bridge("Copy", value.markdown);
        notify("已复制团队 Markdown");
      });
    });
  }
  return { home };
}
