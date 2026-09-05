import { ref } from "vue";

export const scopeSwitching = ref(false);
let generation = 0;
let pending = 0;

function settle(epoch: number): void {
  // Let mounted hooks, chained requests and their Vue updates finish first.
  setTimeout(() => {
    if (epoch === generation && pending === 0) scopeSwitching.value = false;
  }, 0);
}

export function beginScopeTransition(): void {
  generation++;
  pending = 0;
  scopeSwitching.value = true;
  settle(generation);
}

export function trackScopeRequest(): () => void {
  if (!scopeSwitching.value) return () => {};
  const epoch = generation;
  pending++;
  let finished = false;
  return () => {
    if (finished || epoch !== generation) return;
    finished = true;
    pending--;
    settle(epoch);
  };
}
