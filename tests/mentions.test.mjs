import test from "node:test";
import assert from "node:assert/strict";
import { mentionAt, mentionToken } from "../web/mentions.js";

test("a room mention follows the caret, including in Chinese text", () => {
  assert.deepEqual(mentionAt("请@小看一下", 3), { start: 1, end: 3, query: "小" });
  assert.deepEqual(mentionAt("@Ada\n＠", 6), { start: 5, end: 6, query: "" });
  assert.equal(mentionAt("name@example", 12), null);
  assert.equal(mentionAt("@Bo ", 4), null);
  assert.equal(mentionAt("@Bo", 1, 3), null);
});

test("only ambiguous room member names use a routing ID", () => {
  const members = ["审阅", "QA", "qa", "QA Lead", "Joanne", "Jo"].map((name, i) => ({ id: "member-" + i, name }));
  assert.deepEqual(members.map((bot) => mentionToken(bot, members)),
    ["审阅", "member-1", "member-2", "member-3", "Joanne", "Jo"]);
});
