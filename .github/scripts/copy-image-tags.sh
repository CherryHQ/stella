#!/usr/bin/env bash
set -euo pipefail

# Cross-registry blob transfer happens once. Remaining aliases use the manifest
# already in the destination repository, avoiding another remote copy per tag.
if [ "$#" -lt 3 ]; then
  echo "usage: $0 SOURCE DESTINATION_REPOSITORY TAG [TAG...]" >&2
  exit 1
fi
source_image=$1
destination=$2
shift 2
first_tag=$1
shift
docker buildx imagetools create -t "$destination:$first_tag" "$source_image"
if [ "$#" -gt 0 ]; then
  aliases=()
  for tag in "$@"; do aliases+=("-t" "$destination:$tag"); done
  docker buildx imagetools create "${aliases[@]}" "$destination:$first_tag"
fi
