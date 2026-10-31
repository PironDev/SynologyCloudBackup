#!/usr/bin/env python3
"""Рендерит PNG иконок пакетов из SVG-исходников packaging/icons/src/<бренд>.svg
в packaging/icons/<бренд>/icon_<размер>.png. Существующие PNG перезаписываются.
Нужен rsvg-convert (brew install librsvg). DSM принимает для иконок пакета только PNG,
поэтому SVG — исходник, PNG — результат."""
import pathlib, shutil, subprocess, sys

SIZES = (16, 24, 32, 48, 64, 72, 256)
root = pathlib.Path(__file__).parent

if shutil.which("rsvg-convert") is None:
    sys.exit("rsvg-convert не найден: brew install librsvg")

for svg in sorted((root / "src").glob("*.svg")):

    out = root / svg.stem
    out.mkdir(exist_ok=True)
    for s in SIZES:
        subprocess.run(
            ["rsvg-convert", "-w", str(s), "-h", str(s), "-o", str(out / f"icon_{s}.png"), str(svg)],
            check=True,
        )
        print(f"{out / f'icon_{s}.png'}")
