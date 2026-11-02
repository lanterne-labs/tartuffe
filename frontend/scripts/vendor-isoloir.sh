#!/bin/sh
# Rebuilds and vendors @isoloir/web from a checkout of lanterne-labs/isoloir
# (ISOLOIR_SRC, default: the isoloir checkout beside the mirror
# repository, found from the repository's git directory so that a worktree
# finds it too). The package is not on npm: the tarball in
# frontend/vendor/ is what the build installs; run npm install after.
set -eu
here=$(cd "$(dirname "$0")/.." && pwd)
repo=$(cd "$(git -C "$here" rev-parse --path-format=absolute --git-common-dir)/.." && pwd)
src=${ISOLOIR_SRC:-$repo/../isoloir}
[ -d "$src/js" ] || { echo "no isoloir checkout at $src (set ISOLOIR_SRC)"; exit 1; }
cd "$src/js"
npm ci
npm run wasm
npm run build
rm -f "$here"/vendor/isoloir-web-*.tgz
npm pack --pack-destination "$here/vendor"
ls "$here"/vendor
