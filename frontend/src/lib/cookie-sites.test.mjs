import assert from "node:assert/strict";
import test from "node:test";
import { cookieSites } from "./cookie-sites.ts";

test("only added domains are shown, without cookie secrets", () => {
 const content = "# Netscape HTTP Cookie File\n#HttpOnly_.YouTube.com\tTRUE\t/\tTRUE\t0\tSID\tsecret-one\n.youtube.com\tTRUE\t/\tTRUE\t1\tother\tsecret-two\n.x.com\tTRUE\t/\tTRUE\t9999\tauth\tsecret-three\n.youtube.com.evil.example\tTRUE\t/\tTRUE\t0\tname\tsecret-four\n";
 const sites = cookieSites(content, 100);
 assert.deepEqual(sites, [
  { domain: "x.com", count: 1, expired: 0, youtube: false },
  { domain: "youtube.com", count: 2, expired: 1, youtube: true },
  { domain: "youtube.com.evil.example", count: 1, expired: 0, youtube: false },
 ]);
 assert.ok(!JSON.stringify(sites).includes("secret"));
});

test("editing/removing cookies updates the summary and invalid rows do not invent sites", () => {
 assert.deepEqual(cookieSites(""), []);
 assert.deepEqual(cookieSites("# Netscape HTTP Cookie File\nSID=abc\n.foo.com\tTRUE\t/\tTRUE\tbad\tSID\tx"), []);
 assert.equal(cookieSites("www.youtube.com\tFALSE\t/\tTRUE\t0\tSID\tx\r\n")[0].youtube, true);
});
