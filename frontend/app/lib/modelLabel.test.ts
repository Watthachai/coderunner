// Runs under Node's own test runner, which executes .ts natively:
//   node --test app/lib/modelLabel.test.ts
// Excluded from tsconfig: Node needs the ".ts" import extension below, which
// tsc refuses without allowImportingTsExtensions.
import { test } from "node:test";
import assert from "node:assert/strict";
import { claudeLabel, modelLabel } from "./modelLabel.ts";

test("modelLabel turns model ids into names", () => {
  assert.equal(modelLabel("claude-opus-5-5"), "Opus 5.5");
  assert.equal(modelLabel("claude-opus-5"), "Opus 5");
  assert.equal(modelLabel("claude-sonnet-5"), "Sonnet 5");
  assert.equal(modelLabel("claude-fable-5-1"), "Fable 5.1");
});

test("modelLabel leaves anything else as it is", () => {
  assert.equal(modelLabel("opus"), "opus"); // an env alias
  assert.equal(modelLabel("claude-opus-4-5-20251101"), "claude-opus-4-5-20251101");
  assert.equal(modelLabel(""), "");
});

test("claudeLabel joins model and effort, and is empty when nothing ran", () => {
  assert.equal(claudeLabel("claude-opus-5-5", "xhigh"), "Opus 5.5 · xhigh");
  assert.equal(claudeLabel("claude-opus-5", ""), "Opus 5");
  assert.equal(claudeLabel(undefined, undefined), "");
  assert.equal(claudeLabel("", "xhigh"), "");
});
