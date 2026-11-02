#!/bin/sh
# Driverforge CLI installer (macOS / Linux). Windows users: use Scoop.
#
# Usage:
#   sh -c "$(curl -fsSL https://go.driverforge.com/get)"
#   DRIVERFORGE_VERSION=1.2.3 sh -c "$(curl -fsSL https://go.driverforge.com/get)"
#   DRIVERFORGE_INSTALL_DIR=$HOME/.local/bin sh -c "$(curl -fsSL https://go.driverforge.com/get)"
#
# The command-substitution form, rather than `curl … | sh`, for two reasons: the
# download completes before a single line runs, and stdin stays attached to the
# terminal so the prompts below have somewhere to read from. `-f` matters too —
# without it curl hands an HTTP error page to the shell to execute.
#
# Downloads the release archive from releases.driverforge.com (the canonical
# source), verifies it against a signed release manifest, and installs `driverforge`.
#
# Trust chain, the same one the CLI itself applies to the SDK and agent
# (internal/releaseverify): the public key below is embedded in this script, it
# verifies the detached RS256 signature over the release manifest, and the verified
# manifest carries the SHA-256 of every artifact. So the archive is chained back to
# a key whose private half signs only inside a KMS. Fetching the checksum
# from the same host as the archive — which this script used to do — proves only
# that the two agree, which an attacker who can write one can arrange for both.
#
# Everything here fails closed: a missing signature, an unknown key id, or a digest
# mismatch aborts the install rather than warning and continuing.
#
# Interactivity follows stdin: a terminal gets the prompts, anything else (CI, a
# wizard driving this, a redirect) takes every default rather than stopping on a
# question nobody can answer. DRIVERFORGE_NONINTERACTIVE=1 forces the latter.
#
# This file is published by .github/workflows/release.yml to
# .../driverforge-releases/driverforge-cli/install.sh (mutable, what go/get resolves
# to) and to .../<tag>/install.sh (immutable, per release). Keep the usage lines
# above pointing at the short link: the direct blob URL is an implementation detail
# that has already moved once.

# Braced so the shell parses to the closing brace before running anything: a
# truncated download is then a syntax error rather than a half-run installer that
# moved the binary into place but never reached the chmod.
{

  set -eu

  BIN="driverforge"
  ARTIFACT_BASE="https://releases.driverforge.com/driverforge-releases/driverforge-cli"

  # --- presentation ----------------------------------------------------------
  # Escapes only when stdout is a terminal, so redirected output stays clean.
  if [ -t 1 ]; then
    B=$(printf '\033[1m')
    D=$(printf '\033[2m')
    G=$(printf '\033[32m')
    Y=$(printf '\033[33m')
    R=$(printf '\033[31m')
    Z=$(printf '\033[0m')
  else
    B='' D='' G='' Y='' R='' Z=''
  fi

  say() { printf '%s\n' "$*"; }
  ok() { printf '  %s✓%s %s\n' "$G" "$Z" "$*"; }
  note() { printf '  %s!%s %s\n' "$Y" "$Z" "$*"; }
  err() {
    printf '\n  %s✗ install failed%s\n    %s\n\n' "$R" "$Z" "$*" >&2
    exit 1
  }
  have() { command -v "$1" >/dev/null 2>&1; }

  # ask QUESTION DEFAULT(y|n) — true for yes. Without a terminal the default is
  # taken silently: an installer that blocks in CI is worse than one that assumes.
  INTERACTIVE=0
  if [ -z "${DRIVERFORGE_NONINTERACTIVE:-}" ] && [ -t 0 ]; then
    INTERACTIVE=1
  fi
  ask() {
    _def="$2"
    if [ "$INTERACTIVE" -eq 0 ]; then
      [ "$_def" = y ]
      return
    fi
    if [ "$_def" = y ]; then _hint="[Y/n]"; else _hint="[y/N]"; fi
    printf '  %s %s%s%s ' "$1" "$D" "$_hint" "$Z"
    IFS= read -r _ans || _ans=''
    case "$_ans" in
      '') [ "$_def" = y ] ;;
      [Yy] | [Yy][Ee][Ss]) true ;;
      *) false ;;
    esac
  }

  # Public halves of the release signing keys. The private halves never reach a
  # build machine, so there is nothing secret here — this is exactly the value
  # that should be reviewable in source, since it is the trust root for the binary
  # this script installs. Mirrors trustedKeys in
  # internal/releaseverify/signature.go; the two must agree.
  #
  # release-signing-v1 signed releases up to v0.8.5 and signs nothing new;
  # release-signing-v2 signs every release after it.
  #
  # Rotation: the signature sidecar names its key id, so add the new key as another
  # case in pubkey_for() and keep the old one until no signed manifest references it.
  KEY_ID_V1="release-signing-v1"
  PUBKEY_V1='-----BEGIN PUBLIC KEY-----
MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEAtqHcm7GC4GjkU3Or50E8
NY7IqTnIQdM6Rwubu1URuGD2gLUx3gPFb7JLvXM/X4hDe8PZvVPxmFb9spdHdL2h
iWAqvC+XcjibcRHV+McAhS4VaE5G7RaATXBJOnQBvTg7ztZ/MzYO02FDu1pJsE3o
f9ycCCXX8EsOwDdJ9ecaZb7TElqyIrWkM5dvbJSaldb+YXB7KjpPgwwCnsW8lF+G
WRSsCcuFkEUs9ZQQxnP6QWRyHIByZ6qM8l/zDOCKZD70rCz0jKBr7oRVkjvxJ2nG
ZTf7bSPrDK6uCdGH3EwRoEZJemHnAvjv1HwZRKdOTAElsU2AEKpzf5lnWXRMS+3V
OQIDAQAB
-----END PUBLIC KEY-----'
  KEY_ID_V2="release-signing-v2"
  PUBKEY_V2='-----BEGIN PUBLIC KEY-----
MIICIjANBgkqhkiG9w0BAQEFAAOCAg8AMIICCgKCAgEAyZE6JXIc9y04KJc4y9no
Tst3j//GKRAkvEkrtB0rCq7Nj8bu94O+xQSizFxtYUW9PeI3TaiHLUBtcbaiBglF
hG6Q2mfPZuvFWdwE1I1+Sun7bU+xz27IQkSNiuVSLYhdXojFyPj4o21U2RFGUv6j
n6tyi/0aZvuzmKlwxb8ooNNQCLK8y4aagZmKbYGCVEtACFweuMnppDAzzet+nVgZ
bqvRbGO5LF5UKtUum0N9tG1EKNVUtywAGzyUI9rv6WkPo5d9y3DMS/6RnP/eNojR
DTCNFE6E8t7A/pmXvY+OMhiqhfb5/6S+0ILW0p2kzomIRU5whD9dsW5uUWGuw/sF
gXobjPPVyUrNceUNZZLEFW1VZPzXy2inVP6gbSsWB/VgQISD/ZOB8yqjAAYvzeqn
p/+iaRc+HXlC5Tscukn/NfYbnfitGSq0bmWPqofX1PwaXt/lHPgwiuH+PXqHIWVg
Aj8471t0/gjO8NXXsPITFdJJ4DVgyj61gtwEUT78XEn7cDag+Ix00KxKW19kwuTl
rRfNl6vOIeCzuSPaCaMjJJuY16L3IrFigtNmYzf5k5AALextXLWHbt4NAzr9MLU7
UFwiEUs/FECtpeTqksv0wMXYfFnFWLLLUnPeW5gO7mXlNqVyV3b/+ptQrDgShE6i
/a+jFZUfYYHGYhaFmcEX8hMCAwEAAQ==
-----END PUBLIC KEY-----'

  # pubkey_for echoes the embedded public key for a signature's key id, or nothing
  # when the id is unknown — which the caller must treat as fatal. Selecting by id
  # rather than trying every key means a rotated-out key stops being trusted the
  # moment it is removed here.
  pubkey_for() {
    case "$1" in
      "$KEY_ID_V1") printf '%s\n' "$PUBKEY_V1" ;;
      "$KEY_ID_V2") printf '%s\n' "$PUBKEY_V2" ;;
      *) return 1 ;;
    esac
  }

  # json_str extracts a top-level string field. The manifest and its sidecar are
  # produced by our own release workflow (jq, pretty-printed), so this stays a
  # grep/sed read rather than adding a jq dependency to the bootstrap path.
  json_str() {
    sed -n "s/.*\"$1\"[[:space:]]*:[[:space:]]*\"\([^\"]*\)\".*/\1/p" "$2" | head -1
  }

  # manifest_sha256 echoes the SHA-256 the manifest records for an artifact name.
  # Objects are flattened one-per-line first so the read does not depend on how the
  # JSON happens to be indented, and the name is matched with its surrounding quotes
  # so one asset cannot match another whose name it is a prefix of.
  manifest_sha256() {
    tr -d '\n' <"$2" | sed 's/},/}@/g' | tr '@' '\n' |
      grep -F "\"$1\"" |
      sed -n 's/.*"sha256"[[:space:]]*:[[:space:]]*"\([0-9a-fA-F]\{64\}\)".*/\1/p' |
      head -1
  }

  # sha256_of echoes a file's SHA-256, using whichever tool the platform provides.
  sha256_of() {
    if have sha256sum; then
      sha256sum "$1" | awk '{print $1}'
    elif have shasum; then
      shasum -a 256 "$1" | awk '{print $1}'
    else
      err "need sha256sum or shasum to verify the download"
    fi
  }

  # fetch retries: a release install failing on one dropped connection is a bad
  # first impression, and these are small files on a CDN.
  fetch() { curl -fsSL --retry 3 --retry-delay 1 "$1" -o "$2"; }

  # human_size keeps small archives from rendering as "0.0 MB".
  human_size() {
    awk -v b="$1" 'BEGIN { if (b < 1048576) printf "%.0f KB", b / 1024; else printf "%.1f MB", b / 1048576 }'
  }

  say ""
  say "  ${B}Driverforge CLI${Z}"
  say "  ${D}────────────────${Z}"
  say ""

  # --- platform --------------------------------------------------------------
  os=$(uname -s)
  case "$os" in
    Linux) OS=linux ;;
    Darwin) OS=darwin ;;
    *) err "unsupported OS '$os' (Windows: use 'scoop install driverforge')" ;;
  esac

  arch=$(uname -m)
  case "$arch" in
    x86_64 | amd64) ARCH=amd64 ;;
    arm64 | aarch64) ARCH=arm64 ;;
    *) err "unsupported architecture '$arch'" ;;
  esac

  # An x86_64 shell on Apple Silicon is Rosetta translating; install the native
  # build rather than a binary that will be emulated for its whole life.
  if [ "$OS" = darwin ] && [ "$ARCH" = amd64 ] &&
    [ "$(sysctl -n sysctl.proc_translated 2>/dev/null || echo 0)" = "1" ]; then
    ARCH=arm64
  fi

  have curl || err "curl is required"
  have tar || err "tar is required"
  have openssl || err "openssl is required to verify the release signature (or install with 'brew install driverforge/tap/driverforge')"
  ok "Dependencies      ${D}curl, tar, openssl${Z}"

  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT

  # --- fetch and verify the release manifest ---------------------------------
  # Pinning a version reads that release's own immutable manifest rather than
  # `latest`, so --version installs are verified against the document published
  # with them.
  PINNED="${DRIVERFORGE_VERSION:-}"
  if [ -n "$PINNED" ]; then
    MANIFEST_DIR="$ARTIFACT_BASE/v${PINNED#v}"
  else
    MANIFEST_DIR="$ARTIFACT_BASE/latest"
  fi

  fetch "$MANIFEST_DIR/manifest.json" "$tmp/manifest.json" ||
    err "could not fetch the release manifest: $MANIFEST_DIR/manifest.json"
  fetch "$MANIFEST_DIR/manifest.json.sig" "$tmp/manifest.json.sig" ||
    err "release manifest is not signed — refusing to install unverified code"

  alg=$(json_str alg "$tmp/manifest.json.sig")
  key_id=$(json_str keyId "$tmp/manifest.json.sig")
  sig_b64=$(json_str sig "$tmp/manifest.json.sig")

  [ "$alg" = "RS256" ] || err "unexpected signature algorithm '${alg:-none}' — refusing to install"
  [ -n "$sig_b64" ] || err "release signature is empty — refusing to install"

  pubkey_for "$key_id" >"$tmp/pubkey.pem" ||
    err "release manifest is signed with an unknown key '${key_id:-none}' — refusing to install"

  # openssl for the base64 decode too: BSD and GNU base64 disagree on the decode
  # flag, and openssl is already required here.
  printf '%s' "$sig_b64" | openssl base64 -d -A >"$tmp/sig.bin" 2>/dev/null ||
    err "release signature is not valid base64 — refusing to install"

  openssl dgst -sha256 -verify "$tmp/pubkey.pem" -signature "$tmp/sig.bin" "$tmp/manifest.json" >/dev/null 2>&1 ||
    err "release manifest signature is invalid — refusing to install"

  VERSION=$(json_str version "$tmp/manifest.json")
  [ -n "$VERSION" ] || err "could not read the version from the release manifest"
  VERSION="${VERSION#v}"
  TAG="v$VERSION"

  # A pinned version must match what the manifest we just verified actually
  # describes, so a redirected or stale document cannot quietly install something
  # other than what was asked for.
  if [ -n "$PINNED" ] && [ "$VERSION" != "${PINNED#v}" ]; then
    err "asked for ${PINNED#v} but the verified manifest describes $VERSION"
  fi
  ok "Release           ${B}$VERSION${Z} ${D}(signature verified, $key_id)${Z}"

  ASSET="${BIN}_${VERSION}_${OS}_${ARCH}.tar.gz"
  BASE="$ARTIFACT_BASE/$TAG"

  expected=$(manifest_sha256 "$ASSET" "$tmp/manifest.json")
  [ -n "$expected" ] || err "the verified manifest lists no $ASSET for $TAG"

  # --- where it is going, before it goes there -------------------------------
  DIR="${DRIVERFORGE_INSTALL_DIR:-/usr/local/bin}"
  if [ ! -d "$DIR" ] || [ ! -w "$DIR" ]; then
    if [ -w "$(dirname "$DIR")" ]; then
      mkdir -p "$DIR"
    elif [ -z "${DRIVERFORGE_INSTALL_DIR:-}" ]; then
      # Only fall back when the caller did not name a directory: silently
      # installing somewhere else than asked would be worse than failing.
      DIR="$HOME/.local/bin"
      mkdir -p "$DIR"
    fi
  fi

  SUDO=""
  if [ ! -w "$DIR" ]; then
    if [ -x /usr/bin/sudo ]; then
      SUDO=/usr/bin/sudo
    elif have sudo; then
      SUDO=sudo
    else
      err "cannot write to $DIR and sudo is not available — set DRIVERFORGE_INSTALL_DIR to a writable directory"
    fi
    # A password prompt needs a terminal. Failing here beats sudo failing later
    # with a message about the installer's own stdin.
    if [ "$INTERACTIVE" -eq 0 ] && ! $SUDO -n true 2>/dev/null; then
      err "installing to $DIR needs sudo, but there is no terminal to authenticate on — set DRIVERFORGE_INSTALL_DIR to a writable directory (e.g. \$HOME/.local/bin)"
    fi
  fi

  say ""
  say "  ${B}Ready to install${Z}"
  say "    $BIN $VERSION ${D}($OS/$ARCH)${Z}"
  say "    ${D}→${Z} $DIR/$BIN"
  if [ -n "$SUDO" ]; then
    say "    ${D}$DIR is not writable by you; sudo will be used${Z}"
  fi
  say ""

  ask "Continue?" y || {
    say ""
    say "  ${D}Nothing was installed.${Z}"
    say ""
    exit 0
  }
  say ""

  # --- download, verify, install ---------------------------------------------
  fetch "$BASE/$ASSET" "$tmp/$ASSET" || err "download failed: $BASE/$ASSET"
  size=$(human_size "$(wc -c <"$tmp/$ASSET")")
  ok "Downloaded        ${D}$ASSET ($size)${Z}"

  actual=$(sha256_of "$tmp/$ASSET")
  [ "$actual" = "$expected" ] || err "checksum mismatch for $ASSET (expected $expected, got $actual) — refusing to install"
  ok "Checksum          ${D}matches the signed manifest${Z}"

  tar -xzf "$tmp/$ASSET" -C "$tmp" "$BIN" || err "failed to extract $BIN"

  if [ -n "$SUDO" ]; then
    $SUDO mv "$tmp/$BIN" "$DIR/$BIN" || err "could not install to $DIR/$BIN"
    $SUDO chmod +x "$DIR/$BIN" || err "could not make $DIR/$BIN executable"
  else
    mv "$tmp/$BIN" "$DIR/$BIN" || err "could not install to $DIR/$BIN"
    chmod +x "$DIR/$BIN" || err "could not make $DIR/$BIN executable"
  fi
  ok "Installed         ${B}$DIR/$BIN${Z}"

  # --- PATH ------------------------------------------------------------------
  on_path=0
  case ":$PATH:" in *":$DIR:"*) on_path=1 ;; esac

  if [ "$on_path" -eq 0 ]; then
    case "${SHELL:-}" in
      */zsh) PROFILE="${ZDOTDIR:-$HOME}/.zshrc" ;;
      */bash) if [ -f "$HOME/.bashrc" ]; then PROFILE="$HOME/.bashrc"; else PROFILE="$HOME/.bash_profile"; fi ;;
      */fish) PROFILE="" ;;
      *) PROFILE="$HOME/.profile" ;;
    esac

    say ""
    note "$DIR is not on your PATH."
    if [ -z "$PROFILE" ]; then
      # fish uses its own syntax; print the command rather than guess at config.
      say ""
      say "    ${B}fish_add_path $DIR${Z}"
      say ""
    else
      LINE="export PATH=\"$DIR:\$PATH\""
      say ""
      say "    ${D}$LINE${Z}"
      say "    ${D}→ $PROFILE${Z}"
      say ""
      if grep -Fqs "$DIR" "$PROFILE" 2>/dev/null; then
        say "  ${D}Already referenced in $PROFILE — open a new shell to pick it up.${Z}"
        say ""
      elif ask "Add it now?" n; then
        printf '\n# Added by the Driverforge CLI installer\n%s\n' "$LINE" >>"$PROFILE" ||
          err "could not write to $PROFILE"
        say ""
        ok "Added to ${B}$PROFILE${Z} ${D}— open a new shell, or run: . $PROFILE${Z}"
      fi
    fi
  fi

  # --- done ------------------------------------------------------------------
  say ""
  say "  ${G}${B}Driverforge $VERSION is ready.${Z} 🎉"
  say ""
  say "  ${B}Next steps${Z}"
  say "    ${B}driverforge login${Z}   ${D}sign in to Driverforge${Z}"
  say "    ${B}driverforge init${Z}    ${D}set up an existing driver project${Z}"
  say "    ${B}driverforge --help${Z}  ${D}everything else${Z}"
  say ""

} # end of download guard
