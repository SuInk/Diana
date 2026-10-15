// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

const separator = /[\s,，、;；]/u;

/** Quotes preserve phrases; only a complete quoted token is treated as syntax. */
export function parseTriggerWords(raw: string): string[] {
  const words: string[] = [];
  let index = 0;
  while (index < raw.length) {
    if (separator.test(raw[index])) {
      index += 1;
      continue;
    }
    const rest = raw.slice(index);
    const quoted = rest.match(/^("(?:[^"\\]|\\.)*"|“[^”]*”)(?=$|[\s,，、;；])/u)?.[0];
    let word = "";
    if (quoted) {
      if (quoted.startsWith('“')) word = quoted.slice(1, -1);
      else {
        try { word = JSON.parse(quoted) as string; }
        catch { word = quoted.slice(1, -1); }
      }
      index += quoted.length;
    } else {
      const start = index;
      while (index < raw.length && !separator.test(raw[index])) index += 1;
      word = raw.slice(start, index);
    }
    word = word.trim();
    if (word && !words.includes(word)) words.push(word);
  }
  return words;
}

/** Protect existing multiword triggers when loading and saving without edits. */
export function formatTriggerWords(words: string[]): string {
  return words.map(word => /[\s,，、;；"“”\\]/u.test(word) ? JSON.stringify(word) : word).join(", ");
}
