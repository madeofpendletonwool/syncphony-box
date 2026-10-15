#!/bin/bash -e

# Keep the image small (MAD-809: under about 1 GB compressed — the
# smoke test enforces it). The syncphony-box package ships a dpkg
# path-exclude drop-in (/etc/dpkg/dpkg.cfg.d/50-syncphony-slim) so
# nothing installed after it — later substages, nightly updates — brings
# documentation, man pages or translations. This substage removes what
# stage0-2 installed before it, following the same rules: the copyright
# files and the English locale stay.

R="${ROOTFS_DIR}"

# Documentation, except the copyright files.
find "${R}/usr/share/doc" -mindepth 1 ! -type d ! -name copyright -delete 2>/dev/null || true
find "${R}/usr/share/doc" -mindepth 1 -type d -empty -delete 2>/dev/null || true

rm -rf "${R}/usr/share/man/"* "${R}/usr/share/info/"* "${R}/usr/share/lintian/"*

# Translations, except English and the alias table.
find "${R}/usr/share/locale" -mindepth 1 -maxdepth 1 \
	! -name 'en' ! -name 'en_US' ! -name 'locale.alias' \
	-exec rm -rf {} + 2>/dev/null || true

# Chromium's UI strings: the box never shows Chromium's own UI, and its
# en-US pack is the fallback for every other locale.
if [ -d "${R}/usr/lib/chromium/locales" ]; then
	find "${R}/usr/lib/chromium/locales" -type f ! -name 'en-US.pak*' -delete
fi

# Nothing reads the apt list cache offline; the nightly update refreshes
# it before every upgrade.
rm -rf "${R}/var/lib/apt/lists/"*
on_chroot << EOF
apt-get clean
EOF
