// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

// 演示站用的最小 YAML 读写。真实部署里人设 YAML 由后端生成和解析；演示站没有后端，
// 只能在前端模拟。这里只覆盖人设文件用到的那部分写法：映射、列表、`|-` 字面块、
// 带引号或不带引号的标量、注释。为演示站引一个 YAML 库不值当。

type YAMLValue = string | number | boolean | null | YAMLValue[] | { [key: string]: YAMLValue };

export interface YAMLComments {
  /** 按键名给映射里的条目加注释，多行用 \n 分隔。 */
  [key: string]: string;
}

const plainKey = /^[A-Za-z0-9_.-]+$/;

/**
 * 渲染成块式 YAML。多行字符串用 `|-`，blockKeys 里的键（提示词正文）哪怕只有一行也用
 * `|-`，和后端生成的文件长得一样；其余标量能不加引号就不加。
 */
export function toYAML(value: YAMLValue, header = "", comments: YAMLComments = {}, blockKeys: Set<string> = new Set()): string {
  const lines: string[] = [];
  for (const line of header ? header.split("\n") : []) lines.push(line ? `# ${line}` : "#");
  emitValue(value, 0, lines, comments, blockKeys);
  return lines.join("\n") + "\n";
}

function emitValue(value: YAMLValue, indent: number, lines: string[], comments: YAMLComments, blockKeys: Set<string> = new Set()): void {
  const pad = " ".repeat(indent);
  if (Array.isArray(value)) {
    for (const item of value) {
      if (isMapping(item) && Object.keys(item).length) {
        const nested: string[] = [];
        emitValue(item, indent + 2, nested, comments, blockKeys);
        nested[0] = `${pad}- ${nested[0].trimStart()}`;
        lines.push(...nested);
      } else {
        lines.push(`${pad}- ${inlineScalar(item, indent + 2)}`);
      }
    }
    return;
  }
  if (isMapping(value)) {
    for (const [key, item] of Object.entries(value)) {
      for (const line of comments[key]?.split("\n") ?? []) lines.push(line ? `${pad}# ${line}` : "");
      const name = plainKey.test(key) ? key : JSON.stringify(key);
      if ((isMapping(item) && Object.keys(item).length) || (Array.isArray(item) && item.length)) {
        lines.push(`${pad}${name}:`);
        emitValue(item, indent + 2, lines, comments, blockKeys);
      } else if (blockKeys.has(key) && typeof item === "string" && item !== "" && item === item.trim()) {
        lines.push(`${pad}${name}: |-`, ...item.split("\n").map((line) => (line ? `${pad}  ${line}` : "")));
      } else {
        lines.push(`${pad}${name}: ${inlineScalar(item, indent + 2)}`);
      }
    }
    return;
  }
  lines.push(`${pad}${inlineScalar(value, indent)}`);
}

function inlineScalar(value: YAMLValue, indent: number): string {
  if (Array.isArray(value)) return "[]";
  if (isMapping(value)) return "{}";
  if (value === null) return "null";
  if (typeof value !== "string") return String(value);
  if (value.includes("\n")) {
    const pad = " ".repeat(indent);
    return "|-\n" + value.split("\n").map((line) => (line ? pad + line : "")).join("\n");
  }
  return needsQuotes(value) ? JSON.stringify(value) : value;
}

function needsQuotes(value: string): boolean {
  return (
    value === "" ||
    value !== value.trim() ||
    /^(true|false|null|~|yes|no|on|off)$/i.test(value) ||
    /^[-+]?(\d|\.\d)/.test(value) ||
    /^[-?:,[\]{}#&*!|>'"%@`]/.test(value) ||
    /: |\s#/.test(value)
  );
}

function isMapping(value: YAMLValue | undefined): value is { [key: string]: YAMLValue } {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

interface Line {
  indent: number;
  text: string;
  number: number;
}

/** 解析 toYAML 写得出来的那部分 YAML。遇到不支持的写法抛错，报出行号。 */
export function parseYAML(source: string): YAMLValue {
  const raw = source.replace(/\r\n/g, "\n").split("\n");
  const lines: Line[] = [];
  raw.forEach((text, index) => lines.push({ indent: text.length - text.trimStart().length, text, number: index + 1 }));
  let position = 0;

  function skipBlank(): void {
    while (position < lines.length && isBlankOrComment(lines[position].text)) position++;
  }

  function parseBlock(indent: number): YAMLValue {
    skipBlank();
    if (position >= lines.length) return null;
    const first = lines[position];
    if (first.indent < indent) return null;
    return first.text.trimStart().startsWith("- ") || first.text.trim() === "-" ? parseSequence(first.indent) : parseMapping(first.indent);
  }

  function parseMapping(indent: number): YAMLValue {
    const result: { [key: string]: YAMLValue } = {};
    for (skipBlank(); position < lines.length; skipBlank()) {
      const line = lines[position];
      if (line.indent < indent) break;
      if (line.indent > indent) throw new Error(`第 ${line.number} 行缩进不对`);
      const match = /^("(?:[^"\\]|\\.)*"|[^:#][^:]*?):(?:\s+(.*))?$/.exec(line.text.trim());
      if (!match) throw new Error(`第 ${line.number} 行不是「键: 值」的写法`);
      const key = match[1].startsWith('"') ? (JSON.parse(match[1]) as string) : match[1].trim();
      if (key in result) throw new Error(`第 ${line.number} 行的键 ${key} 重复了`);
      position++;
      result[key] = parseValue(match[2] ?? "", indent, line.number);
    }
    return result;
  }

  function parseSequence(indent: number): YAMLValue {
    const result: YAMLValue[] = [];
    for (skipBlank(); position < lines.length; skipBlank()) {
      const line = lines[position];
      if (line.indent < indent) break;
      const body = line.text.trimStart();
      if (line.indent !== indent || !(body.startsWith("- ") || body === "-")) throw new Error(`第 ${line.number} 行应该是列表项`);
      const rest = body.slice(1).trimStart();
      if (/^("(?:[^"\\]|\\.)*"|[^:#"][^:]*?):(\s|$)/.test(rest)) {
        // 列表项是映射：把「- 」换成等宽的空格，按映射接着读。
        lines[position] = { ...line, indent: indent + 2, text: " ".repeat(indent + 2) + rest };
        result.push(parseMapping(indent + 2));
      } else {
        position++;
        result.push(parseValue(rest, indent, line.number));
      }
    }
    return result;
  }

  function parseValue(text: string, indent: number, number: number): YAMLValue {
    const value = stripComment(text).trim();
    if (value === "") return parseBlock(indent + 1);
    if (/^[|>][-+]?$/.test(value)) return parseLiteral(indent, value);
    if (value === "[]") return [];
    if (value === "{}") return {};
    if (value.startsWith('"')) {
      try {
        return JSON.parse(value) as string;
      } catch {
        throw new Error(`第 ${number} 行的引号字符串没写对`);
      }
    }
    if (value.startsWith("'")) {
      if (!value.endsWith("'") || value.length < 2) throw new Error(`第 ${number} 行的引号字符串没写对`);
      return value.slice(1, -1).replace(/''/g, "'");
    }
    if (/^(true|false)$/.test(value)) return value === "true";
    if (/^(null|~)$/.test(value)) return null;
    if (/^-?\d+(\.\d+)?$/.test(value)) return Number(value);
    return value;
  }

  function parseLiteral(parentIndent: number, indicator: string): string {
    const body: string[] = [];
    let blockIndent = -1;
    while (position < lines.length) {
      const line = lines[position];
      if (line.text.trim() === "") {
        body.push("");
        position++;
        continue;
      }
      if (line.indent <= parentIndent) break;
      if (blockIndent < 0) blockIndent = line.indent;
      if (line.indent < blockIndent) break;
      body.push(line.text.slice(blockIndent));
      position++;
    }
    while (body.length && body[body.length - 1] === "") body.pop();
    const text = indicator.startsWith(">") ? body.join(" ") : body.join("\n");
    return indicator.endsWith("-") ? text : `${text}\n`;
  }

  const value = parseBlock(0);
  skipBlank();
  if (position < lines.length) throw new Error(`第 ${lines[position].number} 行多出来了`);
  return value;
}

function isBlankOrComment(text: string): boolean {
  const trimmed = text.trim();
  return trimmed === "" || trimmed.startsWith("#");
}

function stripComment(text: string): string {
  if (text.startsWith('"') || text.startsWith("'")) return text;
  const index = text.search(/\s#/);
  return index >= 0 ? text.slice(0, index) : text;
}
