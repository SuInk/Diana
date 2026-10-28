<script lang="ts">
import { computed, defineComponent, h, ref, type VNode } from "vue";
import { Check, Copy } from "@lucide/vue";
import MermaidDiagram from "./MermaidDiagram.vue";
import { parseMarkdown, safeHref, type MarkdownBlock, type MarkdownInline } from "../lib/markdown";

// 用渲染函数把语法树变成节点：内容始终是文本节点，不经过 innerHTML。
export default defineComponent({
  name: "MarkdownText",
  props: { source: { type: String, required: true } },
  setup(props) {
    const blocks = computed(() => parseMarkdown(props.source));
    const copied = ref(-1);
    let codeIndex = 0;

    async function copyCode(text: string, index: number): Promise<void> {
      try {
        await navigator.clipboard.writeText(text);
        copied.value = index;
        setTimeout(() => { if (copied.value === index) copied.value = -1; }, 1500);
      } catch { /* 剪贴板不可用时用户仍可手动选中复制。 */ }
    }

    function inline(nodes: MarkdownInline[]): (VNode | string)[] {
      return nodes.map((node) => {
        switch (node.type) {
          case "text": return node.text;
          case "br": return h("br");
          case "code": return h("code", node.text);
          case "strong": return h("strong", inline(node.children));
          case "em": return h("em", inline(node.children));
          case "del": return h("del", inline(node.children));
          case "image":
            return h("a", { class: "md-image", href: node.src, target: "_blank", rel: "noopener noreferrer" },
              h("img", { src: node.src, alt: node.alt, loading: "lazy", referrerpolicy: "no-referrer" }));
          case "link":
            return safeHref(node.href)
              ? h("a", { href: node.href, target: "_blank", rel: "noopener noreferrer" }, inline(node.children))
              : h("span", inline(node.children));
        }
      });
    }

    function block(node: MarkdownBlock, tight = false): VNode | (VNode | string)[] {
      switch (node.type) {
        case "paragraph": return tight ? inline(node.children) : h("p", inline(node.children));
        case "heading": return h(`h${Math.min(node.level + 2, 6)}`, inline(node.children));
        case "hr": return h("hr");
        case "quote": return h("blockquote", node.children.map((child) => block(child)));
        case "list": {
          // 项里打头的那段文字不包 <p>，列表才不会被段落间距撑开。
          const items = node.items.map((item) => h("li", item.flatMap((child, i) => block(child, i === 0 && child.type === "paragraph"))));
          return node.ordered ? h("ol", node.start !== 1 ? { start: node.start } : {}, items) : h("ul", items);
        }
        case "code": {
          if (node.lang.toLowerCase() === "mermaid") return h(MermaidDiagram, { source: node.text });
          const index = codeIndex++;
          const done = copied.value === index;
          return h("div", { class: "md-code" }, [
            h("div", { class: "md-code-bar" }, [
              h("span", node.lang || "代码"),
              h("button", { type: "button", class: "md-code-copy", "aria-label": done ? "代码已复制" : "复制代码", title: done ? "已复制" : "复制代码", onClick: () => copyCode(node.text, index) },
                [h(done ? Check : Copy, { size: 13, "aria-hidden": "true" })]),
            ]),
            h("pre", h("code", node.text)),
          ]);
        }
        case "table":
          return h("div", { class: "md-table" }, h("table", [
            h("thead", h("tr", node.head.map((cell, col) => h("th", { style: node.align[col] ? { textAlign: node.align[col] } : undefined }, inline(cell))))),
            h("tbody", node.rows.map((row) => h("tr", row.map((cell, col) => h("td", { style: node.align[col] ? { textAlign: node.align[col] } : undefined }, inline(cell)))))),
          ]));
      }
    }

    return () => {
      codeIndex = 0;
      return h("div", { class: "markdown-text" }, blocks.value.flatMap((node) => block(node)));
    };
  },
});
</script>

<style scoped>
.markdown-text { overflow-wrap: anywhere; }
.markdown-text > :first-child { margin-top: 0; }
.markdown-text > :last-child { margin-bottom: 0; }
.markdown-text :deep(p) { margin: 0 0 .75em; }
.markdown-text :deep(h3), .markdown-text :deep(h4), .markdown-text :deep(h5), .markdown-text :deep(h6) { margin: 1.1em 0 .5em; font-weight: 600; line-height: 1.5; }
.markdown-text :deep(h3) { font-size: 1.15em; }
.markdown-text :deep(h4) { font-size: 1.05em; }
.markdown-text :deep(h5), .markdown-text :deep(h6) { font-size: 1em; }
.markdown-text :deep(ul), .markdown-text :deep(ol) { margin: 0 0 .75em; padding-left: 1.5em; }
.markdown-text :deep(li) { margin: .2em 0; }
.markdown-text :deep(li > ul), .markdown-text :deep(li > ol) { margin: .2em 0 0; }
.markdown-text :deep(blockquote) { margin: 0 0 .75em; padding: 2px 0 2px 12px; border-left: 3px solid var(--border); color: var(--text-secondary); }
.markdown-text :deep(hr) { margin: 1em 0; border: 0; border-top: 1px solid var(--border); }
.markdown-text :deep(a) { color: var(--accent); text-decoration: underline; text-underline-offset: 2px; }
.markdown-text :deep(code) { padding: 1px 5px; border-radius: 5px; background: var(--surface-2); font-family: var(--font-mono, ui-monospace, SFMono-Regular, Menlo, monospace); font-size: .88em; }
.markdown-text :deep(.md-code) { margin: 0 0 .75em; border: 1px solid var(--border); border-radius: 10px; background: var(--surface-2); overflow: hidden; }
.markdown-text :deep(.md-code-bar) { display: flex; align-items: center; justify-content: space-between; padding: 4px 6px 4px 12px; border-bottom: 1px solid var(--border); color: var(--muted); font-size: 11px; }
.markdown-text :deep(.md-code-copy) { display: inline-grid; place-items: center; width: 24px; height: 24px; padding: 0; border: 0; border-radius: 6px; background: transparent; color: inherit; cursor: pointer; }
.markdown-text :deep(.md-code-copy:hover) { background: var(--surface); color: var(--text); }
.markdown-text :deep(pre) { margin: 0; padding: 10px 12px; overflow-x: auto; line-height: 1.6; }
.markdown-text :deep(pre code) { padding: 0; background: transparent; white-space: pre; overflow-wrap: normal; }
.markdown-text :deep(.md-image) { display: inline-block; max-width: 100%; vertical-align: top; }
.markdown-text :deep(.md-image img) { display: block; max-width: min(100%, 420px); max-height: 360px; border-radius: 8px; object-fit: contain; }
.markdown-text :deep(.md-table) { margin: 0 0 .75em; overflow-x: auto; }
.markdown-text :deep(table) { border-collapse: collapse; font-size: .92em; }
.markdown-text :deep(th), .markdown-text :deep(td) { padding: 6px 10px; border: 1px solid var(--border); text-align: left; vertical-align: top; }
.markdown-text :deep(th) { background: var(--surface-2); font-weight: 600; }
</style>
