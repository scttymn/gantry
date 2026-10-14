"""The home page's font variants, from page.html into pages/."""
import re

h = open("page.html").read()
preloads = re.findall(r'<link rel="preload"[^>]*as="font"[^>]*>', h)
swaps = h.count("font-display: swap")
print(f"{len(preloads)} font preloads, {swaps} faces with font-display: swap")

def without(text, parts):
    for p in parts:
        text = text.replace(p, "", 1)
    return text

optional = h.replace("font-display: swap", "font-display: optional")
for name, page in {
    "as-is": h,
    "no-preload": without(h, preloads),
    "optional-preload": optional,
    "optional": without(optional, preloads),
}.items():
    open(f"pages/{name}.html", "w").write(page)
