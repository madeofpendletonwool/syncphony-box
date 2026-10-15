# Releasing

How a version reaches people: in Raspberry Pi Imager (new boxes) and over
the nightly update (existing ones). The why is in
[ADR 0011](adr/0011-releases-and-imager.md) and
[ADR 0012](adr/0012-box-updates.md).

## One-time setup

### 1. The apt signing key

Boxes trust the apt repo through an OpenPGP key: the public half is
committed and shipped inside every image, and the private half is a
repository secret. Make one on a trusted machine (no passphrase, because CI
signs unattended; the secret store protects it):

```sh
export GNUPGHOME="$(mktemp -d)"
gpg --batch --passphrase '' --quick-gen-key \
  'Syncphony Box apt repository <madeofpendletonwool@users.noreply.github.com>' \
  ed25519 sign 5y
KEY="$(gpg --list-keys --with-colons | awk -F: '/^fpr/ { print $10; exit }')"

# The public half: commit it.
gpg --armor --export "${KEY}" > packaging/apt/syncphony-box-archive-keyring.asc

# The private half: the APT_SIGNING_KEY secret.
gpg --armor --export-secret-keys "${KEY}" | gh secret set APT_SIGNING_KEY

# Keep an offline backup of the private key, then:
rm -rf "${GNUPGHOME}"
```

**Rotating it** (expiry, or a leak) takes two releases, because boxes
only trust the key their installed package shipped:

1. Make the new key and commit its public half, but leave the
   `APT_SIGNING_KEY` secret on the **old** key. Release. Boxes verify
   this release with the old key they trust, and it installs the new
   public key.
2. Switch the secret to the new key. Every later release is signed with
   it.

After a leak, whoever holds the old key can sign packages that boxes
which haven't taken step 1 will still accept. Release step 1 quickly,
and reflash any box you're unsure of.

### 2. GitHub Pages

The first release creates the `gh-pages` branch. Then, under the repo's
Settings → Pages: Source **Deploy from a branch**, branch **gh-pages**,
folder **/ (root)**. From then on:

- `https://madeofpendletonwool.github.io/syncphony-box/imager/os_list.json`
  is the Imager listing
- `https://madeofpendletonwool.github.io/syncphony-box/apt` is the apt
  repo

Nothing to do for signing the checksums: cosign signs keylessly with the
workflow's own GitHub identity.

## Cutting a release

1. Make sure `main` is green.
2. In `CHANGELOG.md`, rename `## [Unreleased]` to `## [X.Y.Z]` (add a new
   empty `## [Unreleased]` above it), and commit.
3. Tag and push:

   ```sh
   git tag -a vX.Y.Z -m "Syncphony Box X.Y.Z"
   git push origin vX.Y.Z
   ```

4. The `build` workflow builds and smoke-tests the Pi image and the x86
   ISO, then the `release` job publishes:
   - the GitHub Release: `syncphony-box-vX.Y.Z-arm64.img.xz` (+ `.sha256`),
     `syncphony-box-vX.Y.Z-amd64.iso` (+ `.sha256`),
     `syncphony-box_X.Y.Z_arm64.deb` and `_amd64.deb`, `SHA256SUMS`
     over all four, `SHA256SUMS.sigstore.json`, `os_list.json`, and the
     changelog section as the notes;
   - on `gh-pages`: both .debs added to the apt repo (re-signed), and
     `imager/os_list.json` pointing at the new Pi image (the listing is
     Pi-only; ADR 0013).
5. Boxes pick it up at their next 04:00. To see one take it right away:
   `sudo syncphony-box update` on the box.

The release job refuses to run without the changelog section, the
committed public key or the `APT_SIGNING_KEY` secret.

**If the job fails after creating the GitHub Release** (the Pages push
is the last step), delete the release (`gh release delete vX.Y.Z`, keep
the tag) and re-run the failed job.

## Version numbers

`scripts/version.sh` is the one definition: `git describe` for the image
and boxd (`v1.3.0`, `v1.3.0-4-gabc1234`), and its Debian form for the
.deb (`1.3.0`, `1.3.0+git4.gabc1234`). A dev build sorts after the
release it was cut from and before the next one, so a dev box is never
"updated" back to an older release.

## When an update goes wrong on a box

A failed update changes nothing visible: the box keeps running what it
had, the room's Screens list shows "update failed" (from `/v1/info`), and
the next night retries. Details: `journalctl -u syncphony-update` and
`/data/syncphony-box/update.json`.

If a box doesn't come back after an update: reflash the card with the
current release in Imager (on a PC, reinstall from the current ISO) and
pair it again from the room. There are no
A/B partitions to fall back to (ADR 0012). Please open an issue with
what the box was running (the Screens list shows the version).
