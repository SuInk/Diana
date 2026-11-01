import test from "node:test";
import assert from "node:assert/strict";
import { defaultRoomAppearance, normalizeRoomAppearance, roomAppearanceKey, ROOM_IMAGE_LIMIT } from "./room-customization.ts";
test("room appearance isolates robot keys and rejects absent identity", () => {
 assert.notEqual(roomAppearanceKey("one"), roomAppearanceKey("two"));
 assert.throws(() => roomAppearanceKey(" "));
 assert.equal(roomAppearanceKey("a/b"), "diana:room-appearance:v1:a%2Fb");
});
test("corrupt versions and non-finite coordinates fall back; limits are clamped", () => {
 for (const v of [null, [], "bad", { version: 2 }]) assert.deepEqual(normalizeRoomAppearance(v), defaultRoomAppearance());
 const v = normalizeRoomAppearance({ version: 1, size: 500, bedX: -20, deskY: NaN });
 assert.equal(v.size,45); assert.equal(v.bedX,10); assert.equal(v.deskY,76);
});
test("only bounded raster data URLs are restored; no remote URLs or SVG", () => {
 for (const src of ["https://example.com/a.png", "data:image/svg+xml;base64,AAAA", "javascript:alert(1)", "data:image/png;base64," + "A".repeat(ROOM_IMAGE_LIMIT * 2)]) {
  assert.deepEqual(normalizeRoomAppearance({ version:1,images:{background:src} }).images, {});
 }
 assert.equal(normalizeRoomAppearance({version:1,images:{phone:"data:image/png;base64,AAAA"}}).images.phone, "data:image/png;base64,AAAA");
});
test("uploads reject unsupported types and oversized files before decoding", async () => {
 const { readRoomImage } = await import("./room-customization.ts");
 await assert.rejects(readRoomImage({ type: "image/svg+xml", size: 10 }), /只支持/);
 await assert.rejects(readRoomImage({ type: "image/png", size: ROOM_IMAGE_LIMIT+1 }), /700 KB/);
});
test("a renamed SVG or HTML cannot impersonate a PNG upload", async () => {
 const { readRoomImage } = await import("./room-customization.ts");
 await assert.rejects(readRoomImage(new File(["<svg></svg>"], "fake.png", { type: "image/png" })), /不匹配/);
});
