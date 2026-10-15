import assert from "node:assert/strict";
import test from "node:test";

import {
  accountSafetyRulesOverride,
  effectiveAccountSafetyRules,
  FALLBACK_ACCOUNT_SAFETY_RULES,
} from "./account-safety-rules.ts";

test("account safety editor shows built-in rules for an empty override", () => {
  assert.equal(effectiveAccountSafetyRules("", "内置规则"), "内置规则");
  assert.equal(effectiveAccountSafetyRules(undefined, undefined), FALLBACK_ACCOUNT_SAFETY_RULES);
});

test("unchanged account safety rules stay an empty override while edits are saved", () => {
  assert.equal(accountSafetyRulesOverride("\r\n 内置规则 \r\n", "内置规则"), "");
  assert.equal(accountSafetyRulesOverride(" 自定义规则\r\n第二行 ", "内置规则"), "自定义规则\n第二行");
});
