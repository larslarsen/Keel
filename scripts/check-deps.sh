#!/usr/bin/env bash
# Report direct dependencies that are not on the known list.
#
# Scope is deliberate: only *direct* requires. The realistic supply-chain risk
# is a name nobody chose — a typo, or a module invented by an assistant writing
# an import — being fetched and pinned on first build. That is always a direct
# dependency. Transitive ones are chosen by upstream and their hashes are fixed
# by go.sum, so a known dependency cannot be swapped silently.
#
# An earlier version checked the whole transitive closure and flagged 150+
# legitimate modules. A report that size is not read, which is worse than no
# report at all.
#
# Reports; never fails. A new dependency is a normal event, and a check that
# blocks on one gets switched off.
set -uo pipefail
cd "$(dirname "$0")/../daemon" || exit 0

# Every module this project deliberately depends on. Adding one here should be
# a conscious edit in the same commit that adds the dependency.
KNOWN="
github.com/ipfs/go-cid
github.com/libp2p/go-libp2p
github.com/libp2p/go-libp2p-kad-dht
github.com/libp2p/go-libp2p-pubsub
github.com/multiformats/go-multiaddr
github.com/multiformats/go-multihash
modernc.org/sqlite
"

direct=$(go mod edit -json | python3 -c '
import json,sys
m=json.load(sys.stdin)
for r in m.get("Require") or []:
    if not r.get("Indirect"):
        print(r["Path"])
' 2>/dev/null | sort)

if [ -z "$direct" ]; then
  echo "Could not read direct dependencies."
  exit 0
fi

unknown=""
while read -r mod; do
  [ -z "$mod" ] && continue
  if ! printf '%s\n' $KNOWN | grep -qx "$mod"; then
    unknown="$unknown$mod"$'\n'
  fi
done <<< "$direct"

count=$(printf '%s' "$direct" | grep -c . || true)
if [ -z "$unknown" ]; then
  echo "All $count direct dependencies are on the known list."
  exit 0
fi

echo "Direct dependencies not on the known list:"
echo
printf '%s' "$unknown" | sed 's/^/  /'
echo
echo "Each should be a module someone chose on purpose. If a name here was"
echo "never deliberately added, treat it as a typo-squat: remove it, and check"
echo "what go.sum pinned. Otherwise add it to KNOWN in this script."
