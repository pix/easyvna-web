#!/usr/bin/env python3
"""Shrink README screenshots: 256-colour palette (UI shots are flat colours),
then zopflipng if installed. Usage: optimize.py doc/screenshots/*.png"""
import os
import shutil
import subprocess
import sys

from PIL import Image

for path in sys.argv[1:]:
    before = os.path.getsize(path)
    im = Image.open(path).convert("RGB")
    im.quantize(colors=256, method=Image.Quantize.MEDIANCUT, dither=Image.Dither.NONE).save(path, optimize=True)
    if shutil.which("zopflipng"):
        subprocess.run(["zopflipng", "-y", path, path], check=False,
                       stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    print(f"{os.path.basename(path)}: {before // 1024} KB -> {os.path.getsize(path) // 1024} KB")
