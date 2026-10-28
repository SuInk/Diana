// 管理对话回复用的小型 Markdown 解析器：只产出语法树，由组件用渲染函数生成节点，
// 不拼 HTML、不走 v-html，回复里混进的标签只会当文字显示。覆盖模型常写的那些：
// 标题、段落、列表（可嵌套）、引用、代码块（mermaid 画成图）、分隔线、表格，以及粗体、
// 斜体、删除线、行内代码、图片、链接和裸网址。

export type MarkdownInline =
  | { type: "text"; text: string }
  | { type: "strong" | "em" | "del"; children: MarkdownInline[] }
  | { type: "code"; text: string }
  | { type: "link"; href: string; children: MarkdownInline[] }
  | { type: "image"; src: string; alt: string }
  | { type: "br" };

export type MarkdownAlign = "left" | "center" | "right" | "";

export type MarkdownBlock =
  | { type: "paragraph"; children: MarkdownInline[] }
  | { type: "heading"; level: number; children: MarkdownInline[] }
  | { type: "code"; lang: string; text: string }
  | { type: "list"; ordered: boolean; start: number; items: MarkdownBlock[][] }
  | { type: "quote"; children: MarkdownBlock[] }
  | { type: "hr" }
  | { type: "table"; align: MarkdownAlign[]; head: MarkdownInline[][]; rows: MarkdownInline[][][] };

const fencePattern = /^ {0,3}(`{3,}|~{3,})\s*([\w+-]*)/;
const headingPattern = /^ {0,3}(#{1,6})\s+(.*?)\s*#*\s*$/;
const hrPattern = /^ {0,3}([-*_])(\s*\1){2,}\s*$/;
const listPattern = /^( *)([-*+]|\d{1,9}[.)])\s+(.*)$/;
const quotePattern = /^ {0,3}>\s?(.*)$/;
const tableDividerPattern = /^\s*\|?\s*:?-{1,}:?\s*(\|\s*:?-{1,}:?\s*)*\|?\s*$/;

export function parseMarkdown(source: string): MarkdownBlock[] {
  return parseBlocks(source.replace(/\r\n?/g, "\n").replace(/\t/g, "    ").split("\n"));
}

function parseBlocks(lines: string[]): MarkdownBlock[] {
  const blocks: MarkdownBlock[] = [];
  let i = 0;
  while (i < lines.length) {
    const line = lines[i];
    if (!line.trim()) {
      i++;
      continue;
    }
    const fence = fencePattern.exec(line);
    if (fence) {
      const marker = fence[1];
      const body: string[] = [];
      i++;
      while (i < lines.length && !lines[i].trimStart().startsWith(marker)) body.push(lines[i++]);
      i++; // 没有闭合的代码块吃到结尾，和常见渲染器一致。
      blocks.push({ type: "code", lang: fence[2], text: body.join("\n") });
      continue;
    }
    const heading = headingPattern.exec(line);
    if (heading) {
      blocks.push({ type: "heading", level: heading[1].length, children: parseInline(heading[2]) });
      i++;
      continue;
    }
    if (hrPattern.test(line)) {
      blocks.push({ type: "hr" });
      i++;
      continue;
    }
    if (quotePattern.test(line)) {
      const body: string[] = [];
      while (i < lines.length && lines[i].trim()) {
        const quoted = quotePattern.exec(lines[i]);
        body.push(quoted ? quoted[1] : lines[i]);
        i++;
      }
      blocks.push({ type: "quote", children: parseBlocks(body) });
      continue;
    }
    if (listPattern.test(line)) {
      i = parseList(lines, i, blocks);
      continue;
    }
    if (line.includes("|") && i + 1 < lines.length && tableDividerPattern.test(lines[i + 1])) {
      const head = splitRow(line);
      const align = splitRow(lines[i + 1]).map(alignOf);
      const rows: MarkdownInline[][][] = [];
      i += 2;
      while (i < lines.length && lines[i].trim() && lines[i].includes("|")) {
        const cells = splitRow(lines[i++]);
        rows.push(head.map((_, col) => parseInline(cells[col] ?? "")));
      }
      blocks.push({ type: "table", align: head.map((_, col) => align[col] ?? ""), head: head.map(parseInline), rows });
      continue;
    }
    const body: string[] = [];
    while (i < lines.length && lines[i].trim() && !startsBlock(lines, i)) body.push(lines[i++].trim());
    blocks.push({ type: "paragraph", children: parseInline(body.join("\n")) });
  }
  return blocks;
}

function startsBlock(lines: string[], i: number): boolean {
  const line = lines[i];
  return fencePattern.test(line) || headingPattern.test(line) || hrPattern.test(line) || quotePattern.test(line) || listPattern.test(line)
    || (line.includes("|") && i + 1 < lines.length && tableDividerPattern.test(lines[i + 1]));
}

// parseList 读一整个列表；缩进比当前项标记更深的行归这一项，所以能嵌套。
function parseList(lines: string[], start: number, blocks: MarkdownBlock[]): number {
  const first = listPattern.exec(lines[start])!;
  const indent = first[1].length;
  const ordered = /\d/.test(first[2]);
  const items: MarkdownBlock[][] = [];
  let i = start;
  while (i < lines.length) {
    const match = listPattern.exec(lines[i]);
    if (!match || match[1].length !== indent || /\d/.test(match[2]) !== ordered) break;
    const contentIndent = indent + match[2].length + 1;
    const body = [match[3]];
    i++;
    while (i < lines.length) {
      const line = lines[i];
      if (!line.trim()) {
        // 空行之后还缩进着的才算同一项。
        if (i + 1 < lines.length && leadingSpaces(lines[i + 1]) > indent && lines[i + 1].trim()) {
          body.push("");
          i++;
          continue;
        }
        break;
      }
      const nested = listPattern.exec(line);
      if (nested && nested[1].length <= indent) break;
      if (!nested && leadingSpaces(line) <= indent && startsBlock(lines, i)) break;
      body.push(line.slice(Math.min(leadingSpaces(line), contentIndent)));
      i++;
    }
    items.push(parseBlocks(body));
    if (i < lines.length && !lines[i].trim()) break;
  }
  blocks.push({ type: "list", ordered, start: ordered ? Number.parseInt(first[2], 10) : 1, items });
  return i;
}

function leadingSpaces(line: string): number {
  return line.length - line.trimStart().length;
}

function splitRow(line: string): string[] {
  let row = line.trim();
  if (row.startsWith("|")) row = row.slice(1);
  if (row.endsWith("|") && !row.endsWith("\\|")) row = row.slice(0, -1);
  return row.split(/(?<!\\)\|/).map((cell) => cell.trim().replace(/\\\|/g, "|"));
}

function alignOf(cell: string): MarkdownAlign {
  const left = cell.startsWith(":"), right = cell.endsWith(":");
  if (left && right) return "center";
  if (right) return "right";
  return left ? "left" : "";
}

// 网址结尾的中文标点、右括号常常是句子的一部分，不算进链接。
const urlPattern = /https?:\/\/[^\s<>"'`，。；：！？、）】》「」]+/;
const trailingURLPunctuation = /[.,;:!?)\]}'"]+$/;

const inlineRules: { pattern: RegExp; build: (m: RegExpExecArray) => MarkdownInline | null }[] = [
  { pattern: /\\([\\`*_~[\]()#>|!-])/, build: (m) => ({ type: "text", text: m[1] }) },
  { pattern: /(`+)([\s\S]+?)\1/, build: (m) => ({ type: "code", text: m[2].trim() || m[2] }) },
  // 图片只认 http(s)，挡掉 data:、javascript: 之类。
  {
    pattern: /!\[([^\]\n]*)\]\(\s*<?([^\s)>]+)>?(?:\s+"[^"]*")?\s*\)/,
    build: (m) => (/^https?:\/\//i.test(m[2]) ? { type: "image", src: m[2], alt: m[1] } : null),
  },
  {
    pattern: /\[([^\]\n]+)\]\(\s*<?([^\s)>]+)>?(?:\s+"[^"]*")?\s*\)/,
    build: (m) => (safeHref(m[2]) ? { type: "link", href: m[2], children: parseInline(m[1]) } : null),
  },
  { pattern: urlPattern, build: (m) => ({ type: "link", href: m[0], children: [{ type: "text", text: m[0] }] }) },
  { pattern: /\*\*(?=\S)([\s\S]*?\S)\*\*|__(?=\S)([\s\S]*?\S)__/, build: (m) => ({ type: "strong", children: parseInline(m[1] ?? m[2]) }) },
  { pattern: /~~(?=\S)([\s\S]*?\S)~~/, build: (m) => ({ type: "del", children: parseInline(m[1]) }) },
  // 只认星号斜体：下划线在 snake_case 标识符里太常见。
  { pattern: /\*(?=[^\s*])([^*\n]*?[^\s*])\*/, build: (m) => ({ type: "em", children: parseInline(m[1]) }) },
  { pattern: /\n/, build: () => ({ type: "br" }) },
];

export function parseInline(source: string): MarkdownInline[] {
  const out: MarkdownInline[] = [];
  const pushText = (text: string) => {
    if (!text) return;
    const last = out[out.length - 1];
    if (last?.type === "text") last.text += text;
    else out.push({ type: "text", text });
  };
  let rest = source;
  while (rest) {
    let best: { index: number; length: number; node: MarkdownInline | null } | null = null;
    for (const rule of inlineRules) {
      const match = rule.pattern.exec(rest);
      if (!match || (best && match.index >= best.index)) continue;
      let length = match[0].length;
      let node = rule.build(match);
      if (rule.pattern === urlPattern) {
        const href = match[0].replace(trailingURLPunctuation, "");
        length = href.length;
        node = { type: "link", href, children: [{ type: "text", text: href }] };
      }
      best = { index: match.index, length, node };
    }
    if (!best) {
      pushText(rest);
      break;
    }
    pushText(rest.slice(0, best.index));
    const raw = rest.slice(best.index, best.index + best.length);
    if (best.node?.type === "text") pushText(best.node.text);
    else if (best.node) out.push(best.node);
    else pushText(raw);
    rest = rest.slice(best.index + best.length);
  }
  return out;
}

/** 只放行 http(s) 和 mailto，挡住 javascript: 之类的地址。 */
export function safeHref(href: string): boolean {
  return /^(https?:\/\/|mailto:)/i.test(href.trim());
}

/** 去掉标记只留文字，给列表预览这类单行位置用。 */
export function markdownToPlain(source: string): string {
  const inlineText = (nodes: MarkdownInline[]): string => nodes.map((node) => {
    if (node.type === "text" || node.type === "code") return node.text;
    if (node.type === "image") return node.alt || "[图片]";
    if (node.type === "br") return " ";
    return inlineText(node.children);
  }).join("");
  const blockText = (node: MarkdownBlock): string => {
    switch (node.type) {
      case "paragraph": case "heading": return inlineText(node.children);
      case "code": return node.text;
      case "quote": return node.children.map(blockText).join(" ");
      case "list": return node.items.map((item) => item.map(blockText).join(" ")).join(" ");
      case "table": return [node.head, ...node.rows].map((row) => row.map(inlineText).join(" ")).join(" ");
      case "hr": return "";
    }
  };
  return parseMarkdown(source).map(blockText).filter(Boolean).join(" ").replace(/\s+/g, " ").trim();
}
