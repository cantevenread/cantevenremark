# cantevenReMark icon: a tiny 24x24 pixel-art page + pencil on a classic
# bevelled grey tile (late-90s web/desktop style). Writes AppIcon.icns,
# AppIcon-1024.png and icon.svg (used inside the app).
import io, struct, sys
from PIL import Image

N = 24
C = {
    "W": (255, 255, 255), "L": (223, 223, 223), "G": (192, 192, 192), "D": (128, 128, 128),
    "K": (10, 10, 10), "N": (0, 0, 128), "B": (16, 132, 208), "Y": (255, 214, 0), "O": (201, 140, 0),
    "P": (255, 128, 170), "T": (232, 196, 140), "R": (204, 32, 32), "S": (160, 160, 160),
}
grid = [["G"] * N for _ in range(N)]
def px(x, y, c):
    if 0 <= x < N and 0 <= y < N: grid[y][x] = c

# bevel (raised button)
for i in range(N):
    px(i, 0, "W"); px(0, i, "W"); px(i, N - 1, "K"); px(N - 1, i, "K")
for i in range(1, N - 1):
    px(i, 1, "L"); px(1, i, "L"); px(i, N - 2, "D"); px(N - 2, i, "D")

# page with a folded top-right corner
for y in range(3, 20):
    for x in range(4, 16):
        px(x, y, "W")
for x in range(4, 13): px(x, 3, "K")          # top edge
for x in range(4, 16): px(x, 19, "K")         # bottom edge
for y in range(3, 20): px(4, y, "K")          # left edge
for y in range(6, 20): px(15, y, "K")         # right edge
for y in range(3, 7): px(12, y, "K")          # fold: vertical crease
for x in range(12, 16): px(x, 6, "K")         # fold: horizontal crease
px(13, 4, "K"); px(14, 5, "K")                # fold: diagonal edge
px(13, 5, "S")                                # fold flap
for x in (13, 14, 15): px(x, 3, "G")          # outside the page
for x in (14, 15): px(x, 4, "G")
px(15, 5, "G")
for y in range(7, 21): px(16, y, "D")         # drop shadow
for x in range(5, 17): px(x, 20, "D")
for (y, a, b) in [(9, 6, 11), (11, 6, 13), (13, 6, 12), (15, 6, 10)]:  # text lines
    for x in range(a, b + 1): px(x, y, "N")
for x in range(6, 10):                        # small red "PDF" tag
    px(x, 17, "R")

# pencil, eraser (top right) to point (bottom left), writing on the page
parts = ["P", "P", "S", "Y", "Y", "Y", "Y", "Y", "T", "K"]
for k, c in enumerate(parts):
    x, y = 20 - k, 5 + k
    px(x, y, c)
    if c != "K":
        px(x + 1, y, c)
    if c in "Y":
        px(x + 2, y, "O")

def png(size):
    im = Image.new("RGBA", (N, N))
    for y in range(N):
        for x in range(N):
            im.putpixel((x, y), C[grid[y][x]] + (255,))
    cell = size // N
    big = im.resize((N * cell, N * cell), Image.NEAREST)
    out = Image.new("RGBA", (size, size), (0, 0, 0, 0))
    off = (size - N * cell) // 2
    out.paste(big, (off, off))
    return out

def icns(path):
    entries = [(b"ic07", 128), (b"ic08", 256), (b"ic09", 512), (b"ic10", 1024), (b"ic11", 32), (b"ic12", 64), (b"ic13", 256), (b"ic14", 512)]
    chunks = b""
    for t, s in entries:
        b = io.BytesIO(); png(s).save(b, "PNG"); d = b.getvalue()
        chunks += t + struct.pack(">I", 8 + len(d)) + d
    open(path, "wb").write(b"icns" + struct.pack(">I", 8 + len(chunks)) + chunks)

def svg(path):
    rects = []
    for y in range(N):
        x = 0
        while x < N:
            c = grid[y][x]; w = 1
            while x + w < N and grid[y][x + w] == c: w += 1
            r, g, b = C[c]
            rects.append(f'<rect x="{x}" y="{y}" width="{w}" height="1" fill="#{r:02x}{g:02x}{b:02x}"/>')
            x += w
    open(path, "w").write(f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {N} {N}" shape-rendering="crispEdges">' + "".join(rects) + "</svg>")

out = sys.argv[1] if len(sys.argv) > 1 else "."
png(1024).save(f"{out}/AppIcon-1024.png")
icns(f"{out}/AppIcon.icns")
svg(f"{out}/../ui/icon.svg")
print("ok")
