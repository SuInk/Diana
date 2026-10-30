// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

import test from "node:test";
import assert from "node:assert/strict";
import { llmProviderKinds, defaultPresetForProvider, detectLLMService } from "./llm-presets.ts";

test("Decisions has a dedicated protocol and preserves its preset on reload", () => {
  const preset = defaultPresetForProvider("openai_decisions");
  assert.equal(preset.model, "gpt-6-luna");
  assert.equal(preset.apiStyle, "");
  assert.equal(detectLLMService(preset.baseURL, preset.provider), preset.id);
  assert.equal(detectLLMService("https://gateway.example/v1", preset.provider), "openai-decisions-custom");
  assert.equal(llmProviderKinds.find(kind => kind.id === preset.provider).supportsAPIStyle, false);
  assert.equal(detectLLMService(preset.baseURL, "openai_compatible"), "openai");
});
