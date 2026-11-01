import type { RoomPose } from "./room-activity";

export interface MotionFrame {
  pose: RoomPose;
  location: "bed" | "desk";
  phase: "settled" | "put-away" | "stand" | "walk" | "sit";
}
export function roomMotion(initial: RoomPose, emit: (frame: MotionFrame) => void,
  schedule: (fn: () => void, ms: number) => ReturnType<typeof setTimeout> = setTimeout,
  unschedule: (id: ReturnType<typeof setTimeout>) => void = clearTimeout) {
  let frame: MotionFrame = { pose: initial, location: initial === "desk" ? "desk" : "bed", phase: "settled" };
  let timer: ReturnType<typeof setTimeout> | undefined;
  let generation = 0;
  const publish = (next: MotionFrame) => { frame = next; emit(next); };
  const cancel = () => { generation++; if (timer !== undefined) unschedule(timer); timer = undefined; };
  return {
    cancel,
    target(pose: RoomPose, paused: boolean) {
      cancel();
      const token = generation;
      const location = pose === "desk" ? "desk" : "bed";
      const settled: MotionFrame = { pose, location, phase: "settled" };
      if (paused || (location === frame.location && frame.phase === "settled" && pose === frame.pose)) { publish(settled); return; }
      // Replace rather than queue targets. A stale timer can never finish a newer sequence.
      const steps: Array<[number, MotionFrame]> = location === frame.location
        ? [[0, { ...frame, phase: "sit" }], [350, settled]]
        : [[0, { ...frame, phase: "put-away" }],
          [350, { pose: "rest", location: frame.location, phase: "stand" }],
          [350, { pose: "rest", location, phase: "walk" }],
          [1800, { pose, location, phase: "sit" }], [400, settled]];
      const next = () => {
        if (token !== generation) return;
        const step = steps.shift();
        if (!step) { timer = undefined; return; }
        timer = schedule(() => { if (token !== generation) return; publish(step[1]); next(); }, step[0]);
      };
      next();
    }
  };
}
