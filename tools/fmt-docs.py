#!/usr/bin/env python3
"""pgb docs formatter.

Formats fenced code blocks in the repo's .mdx files:
  go    -> gofmt (canonical; fragments that aren't valid Go are left as-is)
  json  -> json.dumps(indent=2)
  all   -> tabs to 2 spaces for non-Go blocks, trailing whitespace stripped,
           no trailing blank lines before the closing fence

Usage:
  python3 tools/fmt-docs.py            # format in place
  python3 tools/fmt-docs.py --check    # exit 1 if anything would change (CI)

The 72-char line budget is enforced separately by review + the overflow probe;
this tool normalizes style, it does not wrap code automatically.
"""
import json
import pathlib
import re
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
DOCS = ROOT / "docs"
FENCE = re.compile(r"^(\s*)```")
BUDGET = 72  # advisory; reported, not auto-fixed


def gofmt(code: str) -> str:
    r = subprocess.run(["gofmt"], input=code, capture_output=True, text=True)
    return r.stdout if r.returncode == 0 and r.stdout else code


def fmt_block(lang: str, code: str) -> tuple[str, list[str]]:
    """Return (formatted block, notes). `code` excludes the fence lines."""
    notes = []
    lang = (lang or "").strip().lower()
    trailing_nl = code.endswith("\n")

    if lang in ("go", "golang"):
        new = gofmt(code)
        if new != code:
            notes.append("gofmt")
        code = new
    elif lang == "json":
        try:
            new = json.dumps(json.loads(code), indent=2, ensure_ascii=False) + "\n"
            if new != code:
                notes.append("json.reindent")
            code = new
        except (json.JSONDecodeError, ValueError):
            pass  # fragment or template — leave as-is

    # normalization for every language: tabs -> 2 spaces (except gofmt output,
    # which is tab-canonical and rendered with tab-size 2), strip EOL whitespace
    if lang not in ("go", "golang"):
        code = re.sub(r"\t", "  ", code)
    code = "\n".join(line.rstrip() for line in code.split("\n"))
    if trailing_nl and not code.endswith("\n"):
        code += "\n"
    return code, notes


def warns(code: str) -> list[str]:
    out = []
    for i, line in enumerate(code.split("\n"), 1):
        disp = line.replace("\t", "  ")
        if len(disp) > BUDGET:
            out.append(f"    line {i}: {len(disp)} chars > {BUDGET}  {disp.strip()[:60]}")
    return out


def main() -> int:
    check = "--check" in sys.argv
    changed_files = 0
    warn_total = 0

    for mdx in sorted(DOCS.rglob("*.mdx")):
        text = mdx.read_text(encoding="utf-8")
        lines = text.split("\n")
        out = []
        in_fence = False
        fence_indent = ""
        lang = ""
        buf: list[str] = []
        file_changed = False

        for line in lines:
            m = FENCE.match(line)
            if m and not in_fence:
                in_fence = True
                fence_indent, lang = m.group(1), line[m.end():]
                buf = []
                out.append(line)
                continue
            if m and in_fence and line.strip() == "```":
                in_fence = False
                new, notes = fmt_block(lang, "\n".join(buf))
                out.extend(new.split("\n"))
                if new != "\n".join(buf):
                    file_changed = True
                    tag = f" [{', '.join(notes)}]" if notes else ""
                    if not check:
                        print(f"{mdx.relative_to(ROOT)}: reformatted {lang or 'text'} block{tag}")
                for w in warns(new):
                    warn_total += 1
                    print(f"WARN {mdx.relative_to(ROOT)}:{len(out) - len(new.split(chr(10))) + 1}\n{w}")
                out.append(line)
                continue
            if in_fence:
                buf.append(line)
            else:
                out.append(line)

        if file_changed:
            changed_files += 1
            if not check:
                mdx.write_text("\n".join(out), encoding="utf-8")

    verb = "would reformat" if check else "reformatted"
    print(f"{changed_files} file(s) {verb}; {warn_total} line(s) over the {BUDGET}-char advisory budget")
    return 1 if (check and changed_files) else 0


if __name__ == "__main__":
    sys.exit(main())
