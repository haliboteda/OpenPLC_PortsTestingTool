"""The panel dictionary is whole and current (decision 82).

    python tools/check_strings.py

1. Every entry of internal/ptpanel/web/strings.json has zh, en and de, none
   empty, with the same {placeholders} in each.
2. en_of / de_of are the hash of the Chinese each translation was made from:
   Chinese changed without the translation following is a failure, not a
   silently stale panel.
3. Every key the page and the Go code use is in the dictionary, and every
   dictionary key is used.
4. No Chinese outside the dictionary in the page or the Go code that feeds it,
   except the strings listed under not_translated.

Exit 0 = all hold, 1 = something does not.
Decided in $PROD/maps/porttool-panel-languages/issues/LANG-07-how-the-panel-browser-tests-stay-valid.md
and LANG-08-order-against-the-paused-wording-audit.md.
"""

import hashlib
import json
import re
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
WEB = REPO / "internal" / "ptpanel" / "web"
GO_DIRS = [REPO / "internal" / "ptpanel", REPO / "internal" / "simboard"]

CJK = re.compile(r"[一-鿿　-〿＀-￯]")
PH = re.compile(r"\{([a-z0-9_]+)\}")


def short_hash(s):
    return hashlib.sha1(s.encode("utf-8")).hexdigest()[:8]


def js_strings(src):
    """(line, text) of every string and template literal, skipping comments and regexes."""
    out, i, n, prev = [], 0, len(src), ""
    while i < n:
        c = src[i]
        if src.startswith("//", i):
            i = src.find("\n", i)
            i = n if i < 0 else i
            continue
        if src.startswith("/*", i):
            i = src.index("*/", i) + 2
            continue
        if c in "'\"`":
            j = i + 1
            while src[j] != c:
                j += 2 if src[j] == "\\" else 1
            out.append((src.count("\n", 0, i) + 1, src[i:j + 1]))
            prev, i = "x", j + 1
            continue
        if c == "/" and (prev in "(,=:[!&|?{};+-*%<>~^" or prev == ""):
            j, cls = i + 1, False
            while True:
                if src[j] == "\\":
                    j += 2
                    continue
                if src[j] == "[":
                    cls = True
                elif src[j] == "]":
                    cls = False
                elif src[j] == "/" and not cls:
                    break
                j += 1
            prev, i = "x", j + 1
            continue
        if not c.isspace():
            prev = c
        i += 1
    return out


def go_strings(src):
    """(line, text) of every Go string literal, skipping comments."""
    out, i, n = [], 0, len(src)
    while i < n:
        if src.startswith("//", i):
            i = src.find("\n", i)
            i = n if i < 0 else i
            continue
        if src.startswith("/*", i):
            i = src.index("*/", i) + 2
            continue
        c = src[i]
        if c in "\"`'":
            j = i + 1
            while src[j] != c:
                j += 2 if (src[j] == "\\" and c != "`") else 1
            out.append((src.count("\n", 0, i) + 1, src[i:j + 1]))
            i = j + 1
            continue
        i += 1
    return out


def html_text(page):
    """(line, text) of markup text and attributes outside script, style and comments."""
    out = []
    masked = re.sub(r"<script>.*?</script>|<style>.*?</style>|<!--.*?-->",
                    lambda m: "\n" * m.group().count("\n"), page, flags=re.S)
    for no, line in enumerate(masked.split("\n"), 1):
        for t in re.findall(r">([^<>]*)<|\"([^\"]*)\"", line):
            out.append((no, t[0] or t[1]))
    return out


def main():
    problems = []
    doc = json.loads((WEB / "strings.json").read_text(encoding="utf-8"))
    words = doc["strings"]

    for key, e in words.items():
        for lang in ("zh", "en", "de"):
            if not str(e.get(lang, "")).strip():
                problems.append("%s: no %s text" % (key, lang))
        for lang in ("en", "de"):
            if e.get(lang) and sorted(PH.findall(e[lang])) != sorted(PH.findall(e["zh"])):
                problems.append("%s: %s has other {placeholders} than zh" % (key, lang))
            if e.get(lang + "_of") != short_hash(e["zh"]):
                problems.append("%s: the Chinese changed since the %s translation" % (key, lang))

    page = (WEB / "index.html").read_text(encoding="utf-8")
    script = page[page.index("<script>"):page.rindex("</script>")]
    go_files = [p for d in GO_DIRS for p in sorted(d.glob("*.go")) if not p.name.endswith("_test.go")]

    literals = [("index.html", ln, t[1:-1]) for ln, t in js_strings(script)]
    for p in go_files:
        literals += [(p.name, ln, t[1:-1]) for ln, t in go_strings(p.read_text(encoding="utf-8"))]
    html_attr_keys = set(re.findall(r'data-t(?:-placeholder)?="([^"]+)"', page))

    used = {t for _, _, t in literals if t in words} | html_attr_keys
    called = set(re.findall(r'\bT\("([^"]+)"', script))
    for p in go_files:
        called |= set(re.findall(r'\bm\("([^"]+)"', p.read_text(encoding="utf-8")))
    for key in sorted((called | html_attr_keys) - set(words)):
        problems.append("%s: used but not in strings.json" % key)
    for key in sorted(set(words) - used):
        problems.append("%s: in strings.json but used nowhere" % key)

    allowed = {(x["file"].split("/")[-1], x["text"]) for x in doc.get("not_translated", [])}
    for f, ln, t in literals:
        if CJK.search(t) and (f, t) not in allowed:
            problems.append("%s:%d: Chinese outside the dictionary: %s" % (f, ln, t[:40]))
    for ln, t in html_text(page):
        if CJK.search(t):
            problems.append("index.html:%d: Chinese in the markup: %s" % (ln, t[:40]))

    for p in problems:
        print("FAIL  " + p)
    if problems:
        print("%d problem(s) in the panel dictionary" % len(problems))
        return 1
    print("PASS  %d keys, zh/en/de complete and current; de_reviewed=%s"
          % (len(words), str(doc.get("de_reviewed")).lower()))
    return 0


if __name__ == "__main__":
    sys.exit(main())
