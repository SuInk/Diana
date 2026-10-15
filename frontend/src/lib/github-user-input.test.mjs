import assert from "node:assert/strict";
import { test } from "node:test";
import { githubUserLogins, isGitHubLogin } from "./github-user-input.ts";

test("GitHub inputs accept mentions and profile links and deduplicate accounts", () => {
  assert.deepEqual(githubUserLogins("@SuInk, https://github.com/suink/?tab=repositories，github.com/Other\n dependabot[bot]"), ["suink", "other", "dependabot[bot]"]);
  assert.deepEqual(githubUserLogins(""), []);
});

test("repository links and foreign hosts are not silently treated as usernames", () => {
  for (const value of ["https://github.com/acme/demo", "https://github.com.evil.test/alice", "https://other.test/alice", "*", "user/name"]) {
    assert.equal(isGitHubLogin(githubUserLogins(value)[0]), false, value);
  }
  assert.equal(isGitHubLogin("dependabot[bot]"), true);
});
