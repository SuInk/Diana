#!/usr/bin/env python3
"""Conservatively convert fmt.Errorf trailing %v/%s on `err` to %w.

Only rewrites the safest shape:
  fmt.Errorf("...: %v", err)            -> fmt.Errorf("...: %w", err)
  fmt.Errorf("...: %s", err.Error())    -> fmt.Errorf("...: %w", err)
Everything else is left untouched. Compile-time verification (go build)
guards against non-error `err` shadowing.
"""
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent.parent

VERB_RE = re.compile(r"%[#0\- +]*\d*(?:\.\d+)?([vseqxTtg])")


def split_args(text: str):
    """Split a comma-separated argument string at nesting depth 0."""
    args, depth, cur, in_str, esc = [], 0, [], False, False
    for ch in text:
        if in_str:
            cur.append(ch)
            if esc:
                esc = False
            elif ch == "\\":
                esc = True
            elif ch == '"':
                in_str = False
            continue
        if ch == '"':
            in_str = True
            cur.append(ch)
        elif ch in "([{":
            depth += 1
            cur.append(ch)
        elif ch in ")]}":
            depth -= 1
            cur.append(ch)
        elif ch == "," and depth == 0:
            args.append("".join(cur).strip())
            cur = []
        else:
            cur.append(ch)
    if cur and "".join(cur).strip():
        args.append("".join(cur).strip())
    return args


def find_calls(src: str):
    """Yield (start, end, args_text) for each fmt.Errorf(...) call."""
    for m in re.finditer(r"fmt\.Errorf\(", src):
        i = m.end()
        depth, in_str, esc = 1, False, False
        while i < len(src) and depth:
            ch = src[i]
            if in_str:
                if esc:
                    esc = False
                elif ch == "\\":
                    esc = True
                elif ch == '"':
                    in_str = False
            elif ch == '"':
                in_str = True
            elif ch == "(":
                depth += 1
            elif ch == ")":
                depth -= 1
            i += 1
        yield m.start(), i, src[m.end(): i - 1]


def last_verb(fmt: str):
    """Return (start, end, verb) of the last printf verb in the literal."""
    verbs = list(VERB_RE.finditer(fmt))
    # drop %% escapes: a verb match starting at a position preceded by '%'
    verbs = [v for v in verbs
             if not (v.start() > 0 and fmt[v.start() - 1] == "%")]
    if not verbs:
        return None
    v = verbs[-1]
    return v.start(1), v.end(1), v.group(1)


def rewrite(path: Path) -> int:
    src = path.read_text()
    out = []
    cursor = 0
    n = 0
    for start, end, arg_text in find_calls(src):
        out.append(src[cursor:start])
        call = src[start:end]
        if "%w" in call:
            out.append(call)
            cursor = end
            continue
        args = split_args(arg_text)
        new_call = call
        if len(args) >= 2:
            fmt_lit = args[0].strip()
            last = args[-1].strip()
            m = re.fullmatch(r'"((?:[^"\\]|\\.)*)"', fmt_lit)
            if m:
                raw = m.group(1)
                verb = last_verb(raw)
                if verb and verb[2] in ("v", "s"):
                    repl_arg = None
                    if last == "err":
                        repl_arg = None
                    elif last == "err.Error()":
                        repl_arg = "err"
                    if last in ("err", "err.Error()"):
                        raw_new = raw[:verb[0]] + "w" + raw[verb[1]:]
                        fmt_new = '"' + raw_new + '"'
                        new_args = [fmt_new] + args[1:-1]
                        if repl_arg:
                            new_args.append(repl_arg)
                        else:
                            new_args.append(last)
                        new_call = "fmt.Errorf(" + ", ".join(new_args) + ")"
                        n += 1
        out.append(new_call)
        cursor = end
    out.append(src[cursor:])
    if n:
        path.write_text("".join(out))
    return n


def main():
    changed = 0
    files = 0
    for pat in ("webui", "cmd", "model", "internal"):
        for path in sorted((ROOT / pat).rglob("*.go")):
            if path.name.endswith("_test.go"):
                continue
            n = rewrite(path)
            if n:
                files += 1
                changed += n
                print(f"{path.relative_to(ROOT)}: {n}")
    print(f"total: {changed} rewrites in {files} files")


if __name__ == "__main__":
    sys.exit(main())
