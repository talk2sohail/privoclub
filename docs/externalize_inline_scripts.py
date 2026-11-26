"""Move every inline <script> of the built site into its own file, and drop
the theme's inline style attributes.

The readthedocs theme writes small inline scripts on every page (base_url, the
page's name), and colours the links of its bottom bar (GitHub, Previous, Next)
with style="color: #fcfcfc". The documentation site's Content-Security-Policy
allows scripts and styles from the site itself only (script-src 'self',
style-src 'self', no 'unsafe-inline').

So each inline script becomes /_inline/<sha256>.js, named by its content, and
the tag loads it instead. Order and behaviour are unchanged: the tags stay
where they were, without async or defer. The theme's style attribute is
removed; docs/site/theme/privo.css gives those links the same colour.

Fails (exit 1) if any inline script, <style> element or style attribute is
left, so the build fails closed.
Standard library only. Usage: python externalize_inline_scripts.py SITE_DIR
"""

import hashlib
import pathlib
import re
import sys

INLINE = re.compile(r"<script>(.*?)</script>", re.S)
# The only inline style the theme writes (its versions.html).
THEME_STYLE = ' style="color: #fcfcfc"'
# A <style> element, or a style attribute on any tag.
STYLE_LEFT = re.compile(r"<style[\s>]|<[a-zA-Z][^<>]*\sstyle\s*=", re.I)


def main(site: pathlib.Path) -> int:
    out = site / "_inline"
    out.mkdir(exist_ok=True)
    moved = 0
    dropped = 0
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
        dropped += html.count(THEME_STYLE)
        html = html.replace(THEME_STYLE, "")
        left = STYLE_LEFT.search(html)
        if left:
            print(f"{page}: an inline style is left: {left.group(0)[:80]}", file=sys.stderr)
            return 1
        page.write_text(html, encoding="utf-8")
    print(f"moved {moved} inline scripts into {out}; removed {dropped} inline styles")
    return 0


if __name__ == "__main__":
    sys.exit(main(pathlib.Path(sys.argv[1])))
