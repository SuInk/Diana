import test from "node:test";
import assert from "node:assert/strict";
import { roomMotion } from "./room-motion.ts";
function harness() {
  const timers = new Map(); let id = 0; const frames = [];
  const motion = roomMotion("phone", frame => frames.push(frame), fn => { timers.set(++id, fn); return id; }, key => timers.delete(key));
  const tick = () => { const [key, fn] = timers.entries().next().value; timers.delete(key); fn(); };
  return { motion, frames, timers, tick };
}
test("desk sequence puts phone away, stands, walks, sits, then settles", () => {
  const h = harness(); h.motion.target("desk", false);
  while (h.timers.size) h.tick();
  assert.deepEqual(h.frames.map(f => f.phase), ["put-away", "stand", "walk", "sit", "settled"]);
  assert.equal(h.frames[0].pose, "phone"); assert.equal(h.frames[2].pose, "rest"); assert.equal(h.frames.at(-1).pose, "desk");
});
test("new targets replace old work; hidden/reduced motion jumps and cleans timers", () => {
  const h = harness(); h.motion.target("desk", false); h.tick(); h.tick(); h.tick();
  h.motion.target("phone", false);
  while (h.timers.size) h.tick();
  assert.equal(h.frames.at(-1).pose, "phone");
  h.motion.target("desk", false); h.motion.target("rest", true);
  assert.equal(h.timers.size, 0); assert.equal(h.frames.at(-1).pose, "rest");
  h.motion.target("desk", false); h.motion.cancel(); assert.equal(h.timers.size, 0);
});
