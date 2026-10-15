# ADR 0011: Releases and the Raspberry Pi Imager listing

- Status: Accepted
- Date: 2026-10-15
- Deciders: Phase 11 plan (MAD-797), releases issue (MAD-809)

## Context

Until now the image only existed as a CI workflow artifact: you needed a
GitHub login to download it, and nothing told Raspberry Pi Imager about
it, so its customization (Wi-Fi, user, SSH, hostname) only worked if you
picked "Use custom" and happened to know which init format the image
expects. The goal: someone with only Imager and the README goes from
nothing to a playing box.

## Decision

### A tag is a release

The `build` workflow also runs for `v*` tags. The image job is the same
one that runs on every PR (pi-gen on a native arm64 runner, then the
smoke test), so what's released is exactly what was tested. A `release`
job then:

1. Checks its inputs. The repo's apt key (ADR 0012) is committed and the
   `APT_SIGNING_KEY` secret is set, because a release image that can't
   update itself must not ship. `CHANGELOG.md` has a `## [X.Y.Z]` section,
   because a release without notes fails rather than shipping blank.
2. Writes `SHA256SUMS` over the image and the .deb and signs it with
   **cosign, keyless**.
3. Generates the Imager listing.
4. Creates the GitHub Release: `syncphony-box-vX.Y.Z-arm64.img.xz` and
   its `.sha256`, the .deb, `SHA256SUMS` with `SHA256SUMS.sigstore.json`,
   and `os_list.json`, with the changelog section as the notes.
5. Publishes the apt repo and the Imager listing to the `gh-pages`
   branch, which GitHub Pages serves.

The version comes from `git describe` everywhere (`scripts/version.sh`):
the image file name, boxd's `-X main.version` (which `/v1/info` reports)
and, in Debian form, the .deb.

### Signing: cosign keyless, not minisign

The issue allowed minisign or cosign. A minisign key is another long-lived
secret to guard and rotate. Cosign's keyless mode signs with a short-lived
certificate that Sigstore issues to *this workflow at this tag* through
GitHub's OIDC token, and the transparency log records it. There is nothing
to leak, and verifying proves the file came from this repo's release
workflow at a `v*` tag: `cosign verify-blob` with
`--certificate-identity-regexp` pinned to `.github/workflows/build.yml@refs/tags/v`
and the GitHub Actions OIDC issuer. Imager itself checks the image against
the listing's `extract_sha256` while writing, so most people never need
cosign. It's for anyone who wants proof.

The apt repo is the exception: apt only understands OpenPGP, so it has
its own key (ADR 0012).

### The Imager listing

`imager/os_list.json` on GitHub Pages
(`https://madeofpendletonwool.github.io/syncphony-box/imager/os_list.json`)
holds one entry, regenerated on every release by
`scripts/imager-os-list.py`. That script streams the `.img.xz` once for the
download size and SHA-256 and for the extracted size and SHA-256 (all four
of which Imager needs). The URL points at the GitHub Release asset, so the
listing is always the latest release.

- **`init_format: "cloudinit-rpi"`.** The image is Raspberry Pi OS Trixie
  with cloud-init's NoCloud seed on the boot partition (pi-gen's
  `ENABLE_CLOUD_INIT=1`, ADR 0001). That is the format Raspberry Pi's own
  Trixie Lite entry declares in Imager's official list (checked
  2026-10-15). With it, Imager writes `user-data`/`network-config` the way
  this image reads them: username, password, Wi-Fi, SSH, hostname and
  locale all apply. `systemd` (the old `firstrun.sh`) would be silently
  ignored.
- **`devices: ["pi5-64bit", "pi4-64bit"]`**, the boards this image
  supports. Imager hides it for a Pi 3.
- A 40×40 icon, the Syncphony waveform.

People add it under Imager's App Options → Content Repository → custom
URL, or with `rpi-imager --repo <url>`. Getting into Raspberry Pi's own
curated list is a separate, later conversation with them.

### Size: under about 1 GB compressed

The custom stage strips documentation, man pages, info pages, lintian
overrides and translations (except English and Chromium's `en-US.pak`),
keeping the copyright files. The `syncphony-box` package ships the same
rules as a dpkg `path-exclude` drop-in, so packages installed later
(including by the nightly update) never bring them back. The smoke test
fails the build if the `.img.xz` passes 1 GiB.

## Consequences

- GitHub Pages must be enabled for the repo, serving the `gh-pages`
  branch. The first release creates the branch. One-time setup is in
  `docs/releasing.md`.
- The listing always points at the newest release. Older images stay on
  the Releases page.
- A release depends on Sigstore (for signing) and GitHub Pages. If either
  is down, the release job fails before anything public changes, except
  that the GitHub Release is created before the Pages push. A failed Pages
  push is retried by re-running the job after deleting the release.
