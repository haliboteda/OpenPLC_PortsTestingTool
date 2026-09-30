"""Turns one Markdown document into a single self-contained HTML file.

    python tools/md2html.py <in.md> [out.html]

For documents that leave this workspace. Markdown sent to somebody who does
not read Markdown arrives as a screenful of asterisks and pipes, and the tables
this project writes in are the first thing to go.

Self-contained on purpose: the CSS is inline, so the file can be mailed,
dropped in a chat, or opened from a USB stick with nothing alongside it.
There is no PDF step here - a browser's Ctrl+P does that better than a
dependency would, and it is one fewer thing to install.

Exit 0 = written, 1 = could not read or convert, 2 = markdown is not installed.
"""

import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
import tool_repo  # noqa: E402,F401  - finds IAPTranfer_Tool's common.py

from common import Fail, Ok, Section  # noqa: E402

CSS = """
:root { color-scheme: light; }
* { box-sizing: border-box; }
body {
  margin: 0 auto; padding: 32px 28px 64px; max-width: 900px;
  font: 15px/1.75 "Microsoft YaHei", "微软雅黑", -apple-system, Segoe UI, sans-serif;
  color: #1a1d21; background: #fff;
}
h1 { font-size: 26px; margin: 0 0 6px; padding-bottom: 10px; border-bottom: 2px solid #1a1d21; }
h2 { font-size: 20px; margin: 34px 0 10px; padding-top: 4px; }
h3 { font-size: 16px; margin: 24px 0 8px; color: #333; }
p, li { margin: 8px 0; }
code {
  font: 13px/1.5 Consolas, "Cascadia Mono", monospace;
  background: #f2f3f5; padding: 1px 5px; border-radius: 3px; color: #b3261e;
}
pre { background: #f7f8fa; border: 1px solid #e3e5e8; border-radius: 4px;
      padding: 12px 14px; overflow-x: auto; }
pre code { background: none; padding: 0; color: #1a1d21; }
table { border-collapse: collapse; width: 100%; margin: 14px 0; font-size: 14px; }
th, td { border: 1px solid #d6d9dd; padding: 7px 10px; text-align: left;
         vertical-align: top; }
th { background: #f2f3f5; font-weight: 600; }
tr:nth-child(even) td { background: #fbfbfc; }
hr { border: none; border-top: 1px solid #e3e5e8; margin: 30px 0; }
blockquote { margin: 12px 0; padding: 8px 14px; border-left: 3px solid #c7cbd1;
             background: #fafbfc; color: #40454b; }
strong { font-weight: 600; }
a { color: #0b57d0; }
/* One page setup, so Ctrl+P produces something worth sending. */
@media print {
  body { max-width: none; padding: 0; font-size: 11pt; }
  h2, h3 { page-break-after: avoid; }
  table, pre, blockquote { page-break-inside: avoid; }
  a { color: inherit; text-decoration: none; }
}
"""

PAGE = """<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>%(title)s</title>
<style>%(css)s</style>
</head>
<body>
%(body)s
</body>
</html>
"""


def main():
    if len(sys.argv) < 2:
        print(__doc__)
        return 2
    src = Path(sys.argv[1]).resolve()
    dst = Path(sys.argv[2]).resolve() if len(sys.argv) > 2 else src.with_suffix(".html")

    try:
        import markdown
    except ImportError:
        Fail("需要 markdown 库：python -m pip install markdown")
        return 2

    if not src.exists():
        Fail("读不到 " + str(src))
        return 1

    Section("markdown -> html")
    text = src.read_text(encoding="utf-8")
    # The first heading is the title; a browser tab saying "PROD-DOC-REVIEW"
    # tells the reader nothing.
    title = src.stem
    for line in text.splitlines():
        if line.startswith("# "):
            title = line[2:].strip()
            break

    html = markdown.markdown(
        text, extensions=["tables", "fenced_code", "sane_lists", "attr_list"])
    dst.write_text(PAGE % {"title": title, "css": CSS, "body": html},
                   encoding="utf-8")
    Ok("{}  ({}，{:,} 字节)".format(dst, title, dst.stat().st_size))
    print("  发出去之前先在浏览器里看一眼；要 PDF 就在那里 Ctrl+P → 另存为 PDF。")
    return 0


if __name__ == "__main__":
    sys.exit(main())
