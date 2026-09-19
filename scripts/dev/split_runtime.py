#!/usr/bin/env python3
"""Split model/assistant/runtime.go into thematic files by top-level func.

Only whole top-level `func` declarations move; types/vars/consts stay in
runtime.go. New files are named runtime_<theme>.go in the same package.
Import blocks are fixed afterwards by goimports.
"""
import re
import sys
from pathlib import Path

PATH = Path(__file__).resolve().parent.parent.parent / "model/assistant/runtime.go"

THEMES = [
    # (filename suffix, keyword regex on func signature)
    ("outbound", r"outbound|sendOutgoing|sendChunk|delivery|deliverMessage|broadcast|relayMessage|sendText|sendImage|sendVoice|typing|notice|replyMerge|chunk"),
    ("inbound", r"inbound|ingest|enqueue|dequeue|processMessage|handleEvent|onMessage|queueItem|gap"),
    ("llm", r"llm|prompt|completion|chatEvent|buildMessage|messageHistory|contextHistory|token|budget|modelConfig|provider"),
    ("memory", r"memory|recall|semantic|notebook|persona|worldBook|favorability|relationship|portrait"),
    ("image", r"image|avatar|sticker|ocr|media|voice|audio|video"),
    ("tool", r"tool|plugin|agent|schedule|reminder|subscribe|repository|skill"),
]

HEADER = """// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant
"""


def split_decls(src: str):
    """Split source into (header-with-imports, [decls]) at top-level boundaries."""
    lines = src.split("\n")
    # find end of import block
    in_imports = False
    header_end = 0
    for i, line in enumerate(lines):
        if line.startswith("import ("):
            in_imports = True
            continue
        if in_imports and line.startswith(")"):
            header_end = i + 1
            break
    header = "\n".join(lines[:header_end])
    body = "\n".join(lines[header_end:])

    decls = []
    cur = []
    depth = 0
    for line in body.split("\n"):
        is_top = depth == 0 and line.strip() != ""
        if is_top and cur:
            decls.append("\n".join(cur).strip("\n"))
            cur = []
        cur.append(line)
        depth += line.count("{") - line.count("}")
        # grouped decls opened and closed on same pattern handled by depth
    if cur and any(l.strip() for l in cur):
        decls.append("\n".join(cur).strip("\n"))
    return header, [d for d in decls if d.strip()]


def classify(decl: str) -> str:
    first = decl.split("\n", 1)[0]
    if not first.startswith("func "):
        return "core"
    for suffix, pat in THEMES:
        if re.search(pat, first, re.IGNORECASE):
            return suffix
    return "core"


def main():
    src = PATH.read_text()
    header, decls = split_decls(src)
    groups: dict[str, list[str]] = {}
    for d in decls:
        groups.setdefault(classify(d), []).append(d)
    for name, items in sorted(groups.items()):
        print(f"{name}: {len(items)} decls, "
              f"{sum(x.count(chr(10)) + 1 for x in items)} lines")

    out_dir = PATH.parent
    # remaining core decls stay in runtime.go
    core = "\n\n".join(groups.pop("core", []))
    PATH.write_text(header + "\n\n" + core + "\n")

    for name, items in sorted(groups.items()):
        target = out_dir / f"runtime_{name}.go"
        if target.exists():
            print(f"ERROR: {target} already exists", file=sys.stderr)
            sys.exit(1)
        content = HEADER + "\n\n" + "\n\n".join(items) + "\n"
        target.write_text(content)
        print(f"wrote {target.name}: {content.count(chr(10))} lines")


if __name__ == "__main__":
    main()
