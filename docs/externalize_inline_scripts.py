"""Move every inline <script> of the built site into its own file.

The readthedocs theme writes small inline scripts on every page (base_url, the
page's name). The documentation site's Content-Security-Policy allows scripts
from the site itself only (script-src 'self', no 'unsafe-inline'), so each
inline script becomes /_inline/<sha256>.js, named by its content, and the tag
loads it instead. Order and behaviour are unchanged: the tags stay where they
were, without async or defer.

Fails (exit 1) if any inline script is left, so the build fails closed.
Standard library only. Usage: python externalize_inline_scripts.py SITE_DIR
"""

import hashlib
import pathlib
import re
import sys

INLINE = re.compile(r"<script>(.*?)</script>", re.S)


def main(site: pathlib.Path) -> int:
    out = site / "_inline"
    out.mkdir(exist_ok=True)
    moved = 0
    for page in sorted(site.rglob("*.html")):
        html = page.read_text(encoding="utf-8")

        def replace(m: re.Match) -> str:
            nonlocal moved
            body = m.group(1).encode("utf-8")
            name = hashlib.sha256(body).hexdigest()[:32] + ".js"
            (out / name).write_bytes(body)
            moved += 1
            return f'<script src="/_inline/{name}"></script>'

        html = INLINE.sub(replace, html)
        if re.search(r"<script(?![^>]*\bsrc=)[^>]*>", html):
            print(f"{page}: an inline script is left", file=sys.stderr)
            return 1
        page.write_text(html, encoding="utf-8")
    print(f"moved {moved} inline scripts into {out}")
    return 0


if __name__ == "__main__":
    sys.exit(main(pathlib.Path(sys.argv[1])))
