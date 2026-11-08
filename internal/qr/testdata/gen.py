#!/usr/bin/env python3
"""Regenerate the qr package golden files with libqrencode's CLI.

Run from internal/qr:  python3 testdata/gen.py
Needs /usr/bin/qrencode (tested with 4.1.1). The Go tests only read the
generated files and never call qrencode.

* <name>.txt   full symbol: "level X", "input <hex>", optional "minver N",
               then one row per line, '#' dark, '.' light (no quiet zone).
* sweep.txt    every version 1-40 x level L/M/Q/H: the largest byte-mode
               input that still fits that version (qrencode's choice), the
               mask qrencode picked and the SHA-256 of the symbol rows.
"""
import hashlib
import os
import subprocess
import tempfile

HERE = os.path.dirname(os.path.abspath(__file__))

URL250 = ("https://shop.example.com/products/nanovna-h4-antenna-kit?"
          "utm_source=printed-sheet&utm_medium=qr&utm_campaign=autumn-2026&"
          "variant=sma-male-50ohm&ref=easyvna&lang=fr&currency=EUR&"
          "session=0123456789abcdef0123456789abcdef&tracking=ABCDEFGHIJKLMNOPQRSTUVWXYZ"
          "&x=1&y=2&z=3")
URL1000 = "https://example.org/q?" + "&".join(
    "k%03d=v%05d" % (i, i * 7919 % 100000) for i in range(83))

CASES = [  # name, level, input, minimum version (0 = auto)
    ("hello_L", "L", b"hello", 0),
    ("hello_v7_L", "L", b"hello", 7),
    ("amazon_M", "M", b"https://www.amazon.fr/dp/B0C1234567?tag=abcd-21", 0),
    ("url250_Q", "Q", URL250.encode(), 0),
    ("url250_M", "M", URL250.encode(), 0),
    ("utf8_H", "H", "Antenne 2,4 GHz – réglée à 50 Ω ✓ (dipôle)".encode(), 0),
    ("url1000_L", "L", URL1000.encode(), 0),
]


def qrencode(data, level, minver=0):
    with tempfile.NamedTemporaryFile(delete=False) as f:
        f.write(data)
    try:
        cmd = ["qrencode", "-8", "-l", level, "-m", "0", "-t", "ASCII",
               "-r", f.name, "-o", "-"]
        if minver:
            cmd += ["-v", str(minver)]
        out = subprocess.run(cmd, check=True, capture_output=True).stdout
    finally:
        os.unlink(f.name)
    rows = [l[0::2].replace(" ", ".")
            for l in out.decode().splitlines() if l]
    return rows


def version(rows):
    return (len(rows) - 17) // 4


def mask(rows):
    # first format copy: bit i at (x=8, y=i) i<6, (8,7), (8,8), (7,8),
    # then (14-i, 8) for i = 9..14
    pos = [(8, i) for i in range(6)] + [(8, 7), (8, 8), (7, 8)] + \
          [(14 - i, 8) for i in range(9, 15)]
    bits = sum((rows[y][x] == "#") << i for i, (x, y) in enumerate(pos))
    return ((bits ^ 0x5412) >> 10) & 7


def pattern(n):
    return bytes(1 + (i * 37 + 11) % 255 for i in range(n))


def main():
    for name, level, data, minver in CASES:
        rows = qrencode(data, level, minver)
        with open(os.path.join(HERE, name + ".txt"), "w") as f:
            f.write("level %s\ninput %s\n" % (level, data.hex()))
            if minver:
                f.write("minver %d\n" % minver)
            f.write("\n".join(rows) + "\n")
        print(name, "v%d" % version(rows), "mask", mask(rows), len(data), "bytes")

    lines = []
    for level in "LMQH":
        lo = 0
        for v in range(1, 41):
            hi = 3000  # largest n with version(n) <= v, by bisection
            while lo < hi:
                mid = (lo + hi + 1) // 2
                try:
                    ok = version(qrencode(pattern(mid), level)) <= v
                except subprocess.CalledProcessError:
                    ok = False
                lo, hi = (mid, hi) if ok else (lo, mid - 1)
            rows = qrencode(pattern(lo), level)
            assert version(rows) == v, (level, v, lo)
            h = hashlib.sha256(("\n".join(rows) + "\n").encode()).hexdigest()
            lines.append("%s %d %d %d %s" % (level, v, lo, mask(rows), h))
    with open(os.path.join(HERE, "sweep.txt"), "w") as f:
        f.write("# level version maxbytes qrencode-mask sha256(rows)\n")
        f.write("\n".join(lines) + "\n")


if __name__ == "__main__":
    main()
