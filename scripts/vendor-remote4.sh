#!/bin/sh
# Refreshes internal/remote4/assets from a checkout of the platform repo.
#
# The stub driver and laptop runtime are Lua, owned and tested in
# remote4/src in that repo; the CLI embeds a byte-for-byte copy so a
# released binary carries exactly the stub it was tested with.
#
#   scripts/vendor-remote4.sh path/to/platform-checkout
set -eu

ROOT="${1:?usage: scripts/vendor-remote4.sh path/to/platform-checkout}"
SRC="$ROOT/remote4/src"
DEST="$(dirname "$0")/../internal/remote4/assets"

for f in json.lua stub.lua runtime.lua; do
  cp "$SRC/$f" "$DEST/$f"
done

# The broker's private root, as the agent bundles it (anvil-agent/src/lib/ca_cert.lua).
sed -n '/BEGIN CERTIFICATE/,/END CERTIFICATE/p' \
  "$ROOT/anvil-agent/src/lib/ca_cert.lua" > "$DEST/broker-ca.pem"

# The Director callbacks the SDK knows, from its EventHandlers table: the
# stub only defines handler globals the real driver defines, and this list is
# what separates a handler from any other top-level function.
sed -n '/EventHandlers = {/,/^        },/p' "$ROOT/sdk/src/client.lua" \
  | sed -n 's/^ *\([A-Za-z0-9_]*\) = \1,$/\1/p' | sort -u > "$DEST/callbacks.txt"

echo "vendored remote4 assets from $SRC ($(wc -l < "$DEST/callbacks.txt" | tr -d ' ') known callbacks)"
