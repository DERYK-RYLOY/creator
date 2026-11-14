from PIL import Image
import os

srcs = {
    r"C:\Users\Admin\Downloads\creator_scifi_cutout.png": "creator-scifi.png",
    r"C:\Users\Admin\Downloads\creator_gear_cutout.png": "creator-gear.png",
}
dests = [
    r"C:\Users\Admin\Desktop\creator\docs\assets",
    r"C:\Users\Admin\Desktop\creator\cmd\webserver\assets",
]
for d in dests:
    os.makedirs(d, exist_ok=True)

for src, name in srcs.items():
    im = Image.open(src)
    print(name, "orig", im.size, im.mode)
    target_w = 1600
    if im.width > target_w:
        h = round(im.height * target_w / im.width)
        im = im.resize((target_w, h), Image.LANCZOS)
    for d in dests:
        out = os.path.join(d, name)
        im.save(out, optimize=True)
        print("  ->", out, os.path.getsize(out) // 1024, "KB")

# favicons from the gear 'C' on the left of the gear wordmark
ge = Image.open(r"C:\Users\Admin\Downloads\creator_gear_cutout.png")
sq = ge.crop((0, 0, min(ge.width, ge.height), ge.height))
for size in (180, 64, 32):
    f = sq.resize((size, size), Image.LANCZOS)
    for d in dests:
        out = os.path.join(d, "favicon-%d.png" % size)
        f.save(out, optimize=True)
print("favicons written")
