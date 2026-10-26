# The vote circuits (tartuffe-vote/2)

The zero-knowledge circuits of the anonymous vote, specified in
`docs/vote/SPEC.md`. Written in Noir, proved with UltraHonk (Barretenberg)
on the Aztec Ignition setup: no ceremony of ours.

| Directory | What |
|---|---|
| `ballot/` | the ballot statement (SPEC §6.2): membership, pseudonym, ranking rules, encryption |
| `keygen/` | a development tool printing `PK_j = sk_j·G` for a `Prover.toml` of secret keys |

## Versions

- `nargo` 1.0.0-beta.22, the version of `@noir-lang/noir_js` in `frontend/`.
  Install the release binary from github.com/noir-lang/noir into
  `~/.nargo/bin` (the `noirup` installer edits the shell profile).
- Barretenberg 5.0.0: the `bb` binary ships inside `frontend/node_modules/@aztec/bb.js/build/<arch>/bb`.
- `poseidon` library v0.4.0 (noir-lang/poseidon), the Poseidon2 that
  `bb.js`'s `poseidon2Hash` reproduces.

## Commands

```bash
cd circuits/ballot
nargo compile                 # target/ballot.json (ACIR + ABI)
nargo test                    # the rule and packing tests
bb gates -b target/ballot.json
node scripts/consts.mjs dev-keys.txt > src/consts.nr   # new key set → new circuit
cd ../../frontend && node ../circuits/ballot/scripts/vectors.mjs
                              # proves one ballot, checks the encryption
                              # against a JavaScript computation, writes
                              # vectors/dev.json and out/{witness.gz,inputs.json}
```

`src/consts.nr` is generated: the domain tags of SPEC §3 and the election
public keys compiled into the circuit. The committed keys are **development
keys** (`dev-keys.txt`); the setup command regenerates the file with the
event's keys and records the circuit's `vk_hash` in the setup entry.
`vectors/dev.json` holds the vectors of SPEC §14 for the development keys:
the Poseidon2 check value, the tags, a tree, one ballot's inputs, digits,
packed plaintexts, public inputs, ciphertexts and proof.

## Numbers (2026-10-26, M-series Mac, development keys)

| | |
|---|---|
| circuit size | 15,140 gates (1,161 ACIR opcodes); the draft-1 prototype had 50,634 |
| public inputs | 28: event, setup, mask, root, nullifier, seq, 11 points |
| proof | 14,656 B, `noir-recursive` target; verification key 3,680 B |
| witness | 65 ms (noir_js) |
| prove, native `bb` | 0.19 s, 37 MB |
| prove, Chrome 8 threads | 0.34 s |
| prove, Chrome 2 threads | 0.62 s |
| prove, Chrome 1 thread (no cross-origin isolation) | 1.08 s |
| verify | 10 ms native, 40 ms in Node, 100–200 ms in Chrome (includes the key) |

Phones: to measure (beads mirror-8kk.2).
