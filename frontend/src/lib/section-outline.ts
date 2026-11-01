import { onActivated, onBeforeUnmount, onDeactivated, ref, watch, type Ref } from "vue";

export function visibleSectionHeadings(root: HTMLElement): HTMLHeadingElement[] {
  return Array.from(root.querySelectorAll<HTMLHeadingElement>("h2")).filter(heading =>
    heading.getClientRects().length > 0 && Boolean(heading.textContent?.trim())
  );
}

export function activeSectionIndex(tops: number[], offset: number): number {
  let active = 0;
  for (let i = 0; i < tops.length; i++) {
    if (tops[i] <= offset + 8) active = i;
  }
  return tops.length ? active : -1;
}

// Read the rendered headings so conditional platform cards and child components
// cannot drift out of sync with a second, manually maintained navigation list.
export function useSectionOutline(root: Ref<HTMLElement | null>, key: Ref<string>, offset: () => number) {
  const sections = ref<{ id: string; title: string }[]>([]);
  const active = ref("");
  let headings: HTMLHeadingElement[] = [];
  let observer: MutationObserver | null = null;
  let frame = 0;
  let connected = false;

  function updateActive() {
    const index = activeSectionIndex(headings.map(heading => heading.getBoundingClientRect().top), offset());
    active.value = sections.value[index]?.id ?? "";
  }

  function refresh() {
    frame = 0;
    headings = root.value ? visibleSectionHeadings(root.value) : [];
    const next = headings.map((heading, index) => {
      const id = `bot-section-${key.value}-${index}`;
      heading.id = id;
      return { id, title: heading.textContent!.trim() };
    });
    if (JSON.stringify(next) !== JSON.stringify(sections.value)) sections.value = next;
    updateActive();
  }

  function schedule() {
    if (!frame) frame = requestAnimationFrame(refresh);
  }

  function stop() {
    observer?.disconnect();
    observer = null;
    window.removeEventListener("scroll", schedule, true);
    window.removeEventListener("resize", schedule);
    cancelAnimationFrame(frame);
    frame = 0;
    connected = false;
  }

  function start() {
    stop();
    if (!root.value) {
      headings = [];
      sections.value = [];
      active.value = "";
      return;
    }
    connected = true;
    observer = new MutationObserver(schedule);
    observer.observe(root.value, { childList: true, subtree: true, characterData: true, attributes: true, attributeFilter: ["style", "class", "hidden"] });
    window.addEventListener("scroll", schedule, { capture: true, passive: true });
    window.addEventListener("resize", schedule);
    schedule();
  }

  function jump(id: string) {
    const heading = headings[sections.value.findIndex(section => section.id === id)];
    if (!heading) return;
    heading.style.scrollMarginTop = `${offset()}px`;
    heading.tabIndex = -1;
    heading.focus({ preventScroll: true });
    heading.scrollIntoView({ block: "start", behavior: window.matchMedia("(prefers-reduced-motion: reduce)").matches ? "instant" : "smooth" });
    active.value = id;
  }

  watch([root, key], start, { flush: "post" });
  onActivated(() => { if (!connected) start(); });
  onDeactivated(stop);
  onBeforeUnmount(stop);
  return { sections, active, jump };
}
