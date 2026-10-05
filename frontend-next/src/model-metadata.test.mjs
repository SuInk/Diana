import assert from "node:assert/strict";
import { test } from "node:test";
import { applyModelMetadataDraft, createModelMetadataDraft, effectiveModelInfo, mergeModelMetadata, syncModelMetadata } from "./model-metadata.ts";

test("editing a synced model overrides capabilities without changing the directory or the original", () => {
  const models = [{ id: "upstream", context_window_tokens: 1000000, input_modalities: ["text", "image"], output_modalities: ["text"] }];
  const original = JSON.stringify(models);
  const draft = createModelMetadataDraft(models[0]);
  draft.overrideCapabilities = true;
  draft.input = ["text"];
  draft.context = "32000";
  const result = applyModelMetadataDraft(draft, models, "upstream");
  assert.equal(JSON.stringify(models), original);
  assert.equal(result[0].custom, undefined);
  assert.equal(result[0].context_window_tokens, 1000000);
  assert.equal(result[0].context_window_override, 32000);
  assert.deepEqual(effectiveModelInfo(result[0]).input_modalities, ["text"]);
});

test("sync updates directory information but retains edited existing models and custom IDs", () => {
  const overrides = { input_modalities: ["text"], output_modalities: ["text"] };
  const stored = [
    { id: "upstream", capabilities_override: overrides, context_window_override: 8192 },
    { id: "old-cache" },
    { id: "my-alias", custom: true },
    { id: "edited-removed", context_window_override: 24000 }
  ];
  const synced = [{ id: "upstream", input_modalities: ["text", "image"], output_modalities: ["text"], context_window_tokens: 200000 }, { id: "new" }];
  const result = syncModelMetadata(synced, stored);
  assert.deepEqual(result.map(model => model.id), ["upstream", "new", "my-alias", "edited-removed"]);
  assert.equal(result[0].context_window_tokens, 200000);
  assert.equal(result[0].context_window_override, 8192);
  assert.deepEqual(effectiveModelInfo(result[0]).input_modalities, ["text"]);
  assert.deepEqual(effectiveModelInfo(mergeModelMetadata(synced[0], stored[0])).input_modalities, ["text"]);
});

test("clearing an existing model's overrides restores its synced capabilities", () => {
  const models = [{ id: "upstream", input_modalities: ["text", "image"], output_modalities: ["text"], context_window_override: 32000, capabilities_override: { input_modalities: ["text"], output_modalities: ["image"] } }];
  const draft = createModelMetadataDraft(models[0]);
  draft.context = "";
  draft.overrideCapabilities = false;
  const result = applyModelMetadataDraft(draft, models, "upstream");
  assert.equal(result[0].context_window_override, undefined);
  assert.equal(result[0].capabilities_override, undefined);
  assert.deepEqual(effectiveModelInfo(result[0]).input_modalities, ["text", "image"]);
  assert.deepEqual(effectiveModelInfo(result[0]).output_modalities, ["text"]);
});

test("custom metadata accepts explicit capabilities and rejects invalid or duplicate IDs and budgets", () => {
  const draft = createModelMetadataDraft();
  draft.id = "custom-vl";
  draft.context = "64000";
  draft.input.push("image");
  const result = applyModelMetadataDraft(draft, []);
  assert.equal(result[0].custom, true);
  assert.deepEqual(result[0].capabilities_override, { input_modalities: ["text", "image"], output_modalities: ["text"] });
  for (const context of ["-1", "0", "1.5", "1e5", "NaN", "9007199254740992"]) {
    assert.throws(() => applyModelMetadataDraft({ ...draft, context }, []), /正整数/);
  }
  assert.throws(() => applyModelMetadataDraft(draft, result), /已在列表/);
  assert.throws(() => applyModelMetadataDraft({ ...draft, id: "invalid id" }, []), /完整模型/);
  assert.throws(() => applyModelMetadataDraft({ ...draft, output: [] }, []), /输出能力/);
});
