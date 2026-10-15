#!/usr/bin/env python3
"""Write the Raspberry Pi Imager listing for a release (MAD-809, ADR 0011).

Usage:
    scripts/imager-os-list.py IMAGE.img.xz --url URL --icon URL \
        --version VERSION [--release-date YYYY-MM-DD] > os_list.json

Imager reads this file as a custom "Content Repository" (App Options ->
Content Repository, or `rpi-imager --repo <url>`). It needs the sizes and
the SHA-256 of both the download and the extracted image, so this
streams the .img.xz once and computes them all.

init_format is "cloudinit-rpi": the image is Raspberry Pi OS Trixie with
cloud-init's NoCloud seed on the boot partition (pi-gen's
ENABLE_CLOUD_INIT=1), the same as Raspberry Pi's own Trixie entries — so
Imager's username, Wi-Fi, SSH, hostname and locale settings apply.
"""

import argparse
import datetime
import hashlib
import json
import lzma
import os
import sys

CHUNK = 4 << 20


def measure(path):
    """(download size, download sha256, extracted size, extracted sha256)."""
    raw = hashlib.sha256()
    extracted = hashlib.sha256()
    extract_size = 0
    decomp = lzma.LZMADecompressor()
    with open(path, "rb") as f:
        while True:
            chunk = f.read(CHUNK)
            if not chunk:
                break
            raw.update(chunk)
            # xz files can hold several streams (pixz, xz -T); start a new
            # decompressor at each stream boundary.
            while chunk:
                out = decomp.decompress(chunk)
                extracted.update(out)
                extract_size += len(out)
                if decomp.eof:
                    chunk = decomp.unused_data
                    decomp = lzma.LZMADecompressor()
                else:
                    chunk = b""
    return os.path.getsize(path), raw.hexdigest(), extract_size, extracted.hexdigest()


def main():
    p = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    p.add_argument("image")
    p.add_argument("--url", required=True, help="where Imager downloads the .img.xz")
    p.add_argument("--icon", required=True, help="a 40x40 PNG")
    p.add_argument("--version", required=True)
    p.add_argument("--release-date", default=datetime.date.today().isoformat())
    args = p.parse_args()

    download_size, download_sha, extract_size, extract_sha = measure(args.image)
    entry = {
        "name": "Syncphony Box",
        "description": (
            f"Syncphony Box {args.version}: turns a Raspberry Pi plugged into a TV "
            "into a Syncphony room's big screen and speaker (Pi 4 and Pi 5)"
        ),
        "icon": args.icon,
        "url": args.url,
        "extract_size": extract_size,
        "extract_sha256": extract_sha,
        "image_download_size": download_size,
        "image_download_sha256": download_sha,
        "release_date": args.release_date,
        "init_format": "cloudinit-rpi",
        "devices": ["pi5-64bit", "pi4-64bit"],
    }
    json.dump({"os_list": [entry]}, sys.stdout, indent=2)
    sys.stdout.write("\n")


if __name__ == "__main__":
    main()
