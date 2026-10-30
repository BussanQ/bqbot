import test from "node:test";
import assert from "node:assert/strict";
import { createComputerUI } from "../web/computer.js";

function setup(items = [], status = {}) {
  const calls = [], fields = new Map(), nodes = new Map();
  const form = { elements: {} };
  const root = { querySelector: (key) => {
    if (key === "form") return form;
    if (!nodes.has(key)) nodes.set(key, {});
    return nodes.get(key);
  } };
  let html = "", submit;
  globalThis.document = { getElementById: () => root };
  const ui = createComputerUI({
    bridge: async (_, input) => {
      calls.push(input);
      if (input.kind === "computers") return items;
      if (input.kind === "host-status") return status;
      return {};
    },
    openDialog: (_, markup, onSubmit) => {
      html = markup;
      submit = onSubmit;
      form.elements.builtin = { checked: false };
    },
    field: (_, name, value) => { fields.set(name, value); form.elements[name] = { value }; return ""; },
    dialogActions: () => "", getBots: () => [{ id: "bot", name: "伙伴" }],
    notify: () => {}, run: (fn) => fn,
  });
  const click = async (selector, dataset = {}) => {
    const target = { dataset, disabled: false };
    await root.onclick({ target: { closest: (s) => s === selector ? target : null } });
  };
  return { ui, calls, fields, form, nodes, click, get html() { return html; },
    save: (values) => submit(new Map(Object.entries(values))) };
}

test("new local computer hides connection credentials and saves explicit consent", async () => {
  const env = setup();
  await env.ui.home();
  await env.click("[data-config]", { config: "bot" });
  assert.equal(env.fields.get("provider"), "host");
  assert.equal(env.nodes.get("[data-connection]").hidden, true);
  assert.equal(env.form.elements.url.required, false);
  assert.equal(env.form.elements.token_env.disabled, true);
  await env.save({ provider: "host", enabled: "on", host_consent: "on", url: "stale", token_env: "STALE", sandbox_id: "stale" });
  assert.deepEqual(env.calls.at(-1).config, { bot_id: "bot", provider: "host", url: "", token_env: "", sandbox_id: "", enabled: true, host_consent: true });
});

test("legacy local HTTP connections migrate only after explicit selection", async () => {
  const env = setup([{ bot_id: "bot", provider: "host", url: "http://127.0.0.1:8123", token_env: "CUA_TOKEN" }]);
  await env.ui.home();
  await env.click("[data-config]", { config: "bot" });
  assert.equal(env.nodes.get("[data-connection]").hidden, false);
  await env.save({ provider: "host", url: "http://127.0.0.1:8123", token_env: "CUA_TOKEN" });
  assert.equal(env.calls.at(-1).config.url, "http://127.0.0.1:8123");
  env.form.elements.builtin.checked = true;
  env.form.onchange();
  assert.equal(env.nodes.get("[data-connection]").hidden, true);
  await env.save({ provider: "host", builtin: "on", host_consent: "on" });
  assert.equal(env.calls.at(-1).config.url, "");
  assert.equal(env.calls.at(-1).config.token_env, "");
});

test("host status never requests permissions and restart stops before starting", async () => {
  const env = setup([], { available: true, running: true, version: "0.28.2", permissions: { supported: true, accessibility: true, screen_recording: true } });
  await env.ui.home();
  await env.click("[data-host]");
  assert.equal(env.calls.some((v) => v.kind === "host-permissions"), false);
  await env.click("[data-host-action]", { hostAction: "restart" });
  assert.deepEqual(env.calls.slice(-3).map((v) => v.kind), ["host-stop", "host-start", "host-status"]);
  await env.click("[data-permission]", { permission: "screen_recording" });
  assert.deepEqual(env.calls.at(-1), { kind: "host-settings", permission: "screen_recording" });
  await env.click("[data-host-action]", { hostAction: "permissions" });
  assert.deepEqual(env.calls.slice(-2).map((v) => v.kind), ["host-permissions", "host-status"]);
});

test("unsupported platforms report unavailable and escape diagnostics", async () => {
  const env = setup([], { available: false, error: "<script>failure</script>", permissions: { supported: false } });
  await env.ui.home();
  await env.click("[data-host]");
  assert.match(env.html, /目前仅支持 macOS/);
  assert.doesNotMatch(env.html, /data-host-action="permissions"/);
  assert.match(env.html, /&lt;script&gt;/);
  assert.match(env.html, /data-host-action="start" disabled/);
});
