export const workspaceFilesMarkup = `
  <section class="workspace-files" aria-label="工作区文件" hidden>
    <div class="file-toolbar">
      <button type="button" class="secondary" data-files-up>上一级</button>
      <span class="file-path" data-files-path></span>
      <button type="button" class="secondary" data-files-refresh>刷新</button>
      <button type="button" class="secondary" data-files-upload>上传文件</button>
    </div>
    <div class="file-list" data-files-list></div>
    <p class="form-help" role="status" data-files-status></p>
  </section>`;

function fileSize(bytes) {
  if (bytes < 1024) return bytes + " B";
  if (bytes < 1024 ** 2) return (bytes / 1024).toFixed(1) + " KiB";
  return (bytes / 1024 ** 2).toFixed(1) + " MiB";
}

export function bindWorkspaceFiles(panel, call) {
  const find = (name) => panel.querySelector(`[data-files-${name}]`);
  const list = find("list"), status = find("status"), up = find("up");
  let dir = ".", busy = false, loaded = false;

  function controls() {
    for (const b of panel.querySelectorAll("button")) b.disabled = busy;
    up.disabled = busy || dir === ".";
    find("upload").disabled = busy || !loaded;
  }
  function row(entry) {
    const el = document.createElement("div");
    el.className = "file-row";
    const folder = entry.kind === "dir";
    const title = document.createElement(folder ? "button" : "span");
    title.textContent = entry.name;
    title.className = "file-name";
    if (folder) {
      title.type = "button";
      title.dataset.folder = entry.name;
    }
    const info = document.createElement("small");
    info.textContent = folder ? "文件夹" : entry.kind === "file" ? fileSize(entry.size) : "特殊文件";
    el.append(title, info);
    if (entry.kind === "file") {
      const download = document.createElement("button");
      download.type = "button"; download.className = "secondary";
      download.textContent = "下载";
      download.setAttribute("aria-label", "下载 " + entry.name);
      download.dataset.download = entry.name;
      el.append(download);
    }
    return el;
  }
  async function load(next) {
    const entries = await call("files-list", next);
    dir = next; loaded = true;
    find("path").textContent = dir === "." ? "/workspace" : "/workspace/" + dir;
    list.replaceChildren(...entries.map(row));
    status.textContent = entries.length ? "" : "此目录为空";
  }
  async function perform(message, action) {
    if (busy) return;
    busy = true; controls(); status.textContent = message;
    try { await action(); }
    catch (error) { status.textContent = error.message || String(error); }
    finally { busy = false; controls(); }
  }
  const child = (name) => dir === "." ? name : dir + "/" + name;
  function refresh() { return perform("正在读取目录…", () => load(dir)); }
  panel.onclick = (event) => {
    const b = event.target.closest("button");
    if (!b || b.disabled) return;
    if (b.hasAttribute("data-files-refresh")) return refresh();
    if (b === up) return perform("正在读取目录…", () => load(dir.split("/").slice(0,-1).join("/") || "."));
    if (b.dataset.folder) return perform("正在读取目录…", () => load(child(b.dataset.folder)));
    if (b.dataset.download) return perform("正在下载…", async () => {
      status.textContent = await call("file-download", child(b.dataset.download)) ? "文件已保存" : "";
    });
    if (b.hasAttribute("data-files-upload")) return perform("正在上传…", async () => {
      if (!await call("file-upload", dir)) { status.textContent = ""; return; }
      await load(dir); status.textContent = "上传完成";
    });
  };
  find("path").textContent = "/workspace";
  controls();
  return refresh;
}
