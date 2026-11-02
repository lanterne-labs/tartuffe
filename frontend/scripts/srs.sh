#!/usr/bin/env sh
# The SRS the prover needs, served from our own origin (static/srs/, see
# static/coi-sw.js): the head of Aztec's BN254 G1 points for 2^17 points,
# the G2 point, and the Grumpkin G1 points, fetched once from Aztec's CDN.
set -eu
cd "$(dirname "$0")/../static"
mkdir -p srs
CDN=https://crs.aztec-cdn.foundation
G1_BYTES=17825792      # 2^17 points of 64 bytes, plus bb.js's margin, as the prover asks for
GRUMPKIN_BYTES=4194304
fetch() { # path, bytes ('' for all)
	out="srs/$1"
	if [ -n "$2" ]; then
		[ -f "$out" ] && [ "$(wc -c < "$out")" -eq "$2" ] && return 0
		curl -fsSL -r 0-$(( $2 - 1 )) "$CDN/$1" -o "$out"
		[ "$(wc -c < "$out")" -eq "$2" ] || { echo "srs: $1 has $(wc -c < "$out") bytes, want $2" >&2; exit 1; }
	else
		[ -f "$out" ] && return 0
		curl -fsSL "$CDN/$1" -o "$out"
	fi
}
fetch g1_compressed.dat $G1_BYTES
fetch g2.dat ''
fetch grumpkin_g1_v2.dat $GRUMPKIN_BYTES
ls -l srs
