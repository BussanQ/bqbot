import test from "node:test";
import assert from "node:assert/strict";
import { canSteer, reasoningLevels } from "../web/agents.js";

test("a model's empty effort list does not inherit another model's levels", () => {
  const agent = { name: "pi", models: [
    { id: "basic" }, { id: "reasoning", efforts: ["off", "high"] },
  ] };
  assert.deepEqual(reasoningLevels(agent, "basic"), []);
  assert.deepEqual(reasoningLevels(agent, ""), ["off", "high"]);
  assert.deepEqual(reasoningLevels(agent, "reasoning"), ["off", "high"]);
  assert.ok(reasoningLevels(agent, "custom-id").includes("xhigh"));
  assert.deepEqual(reasoningLevels({ name: "echo" }, "old-model"), []);
});

test("a busy private chat can steer only while text can enter the active turn", () => {
  const bot = { name: "pi", steering: true };
  const busy = { busy: true };
  assert.equal(canSteer(busy, bot, []), true);
  for (const change of [{ busy: false }, { room_id: "team" }, { queued: 1 }, { closed: true }, { answering: "another bot" }]) {
    assert.equal(canSteer({ ...busy, ...change }, bot, []), false);
  }
  assert.equal(canSteer(busy, bot, [{ name: "screen.png" }]), false);
  assert.equal(canSteer(busy, { name: "pi", steering: false }, []), false);
  assert.equal(canSteer(busy, { name: "codex" }, []), true);
  assert.equal(canSteer(busy, { name: "claude" }, []), false);
});
