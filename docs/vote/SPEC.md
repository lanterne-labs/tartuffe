# tartuffe-vote/2: encrypted ranked ballots with a public homomorphic tally

Status: **draft 3 for final review**, 2026-10-26. Nothing here is implemented
yet. Draft 1 and draft 2 were each reviewed on 2026-10-26 (adversarial
protocol review ×2, a study of alternatives, a privacy and UX study); this
draft takes in all four and the owner's decisions. It replaces tartuffe-vote/1
(plain anonymous ballots in 30-day windows, `backend/polls`), which never ran
in production.

## 1. Goals

The event is a public, continuous consultation (not an election, not a
sondage). French adults register once with their passport, rank up to three
candidates or vote blank, may change their ballot until the close, and
everyone sees the live results.

Required properties, against the adversaries of §2:

- **P1. Public detection of withholding.** Any third party can detect that a
  ballot a voter cast is missing from the count.
- **P2. No fake ballots.** Every counted ballot comes from a registered key;
  every key comes from one valid passport of an adult French national; a
  passport has at most one key per event; a key's ballot counts once.
- **P3. No single party links a vote to an IP address or to a passport.** Our
  server included.
- **P4. Live results.** Results update throughout the event, as soon as it is
  safe (§9.1), and the page always shows live activity.
- **P5. One period.** A ballot counts until the close or until its voter
  replaces it. Nothing expires.
- **P6. Final at the close.** The final result is written and frozen; nothing
  is accepted after.
- **P7. Versioned**, so the scheme can change later without breaking what is
  logged.
- **P8. No waiting** a voter could be spared: a new voter votes within half a
  minute of the passport scan; downloads and the prover's start happen in the
  background.

Non-goals and accepted residuals (decisions of 2026-10-26):

- coercion resistance and receipt-freeness: a voter can prove a ballot by
  revealing its randomness;
- availability: we can stop the service; the log shows when;
- every relay pooling what it saw with our server (Tor Browser covers it);
- a person holding two valid passports, or an old passport declared lost
  whose chip still reads (ZKPassport's circuits have no revocation), can
  register twice;
- a full copy of a passport chip used to register before its holder
  (ZKPassport has no active authentication); the holder then finds the
  passport taken;
- a site opening a ZKPassport request for our domain with its own `ID`
  (ZKPassport authenticates the domain string, not the web origin: Q1);
- participation is visible to the State (§4);
- at quiet times the first ballot of a new voter can be tied to their
  registration, which links a passport to a pseudonym (§10);
- the quiet-time latency of results: results move only once `K_REAL` ballots
  have changed (§9.1); the page shows the activity meanwhile;
- a group holding `K_REAL − 1` passports can fill a batch around one target's
  change (§10);
- denial of service beyond §7.4.

## 2. Parties and adversaries

| Party | Sees or holds | Trusted for |
|---|---|---|
| Voter's browser | the voter secret `s`, the voter's ballots and their randomness | the voter's own privacy |
| ZKPassport app, bridge and dashboard API | the app reads the chip and proves on the phone; the bridge relays the end-to-end-encrypted request and sees the phone's IP; `dashboard-api.zkpassport.id` is called at request time | proof soundness (open source circuits) |
| Nostr relays (≥ 5, persistent, different operators) | the events posted to them, and the posters' IPs | nothing: they may drop, delete, lie |
| App host: GitHub Pages | readers' IPs | serving the app whose hash is in the setup (A5) |
| Data mirror: a public GitHub repository read through jsDelivr by commit hash | readers' IPs | nothing: everything read is signed and checked |
| Our server (ingest) | the log signing key; reads relays, writes the log, pushes the mirror, posts acknowledgements and checkpoints | **availability only**; it never receives a request from a voter's browser |
| Our tallier | the election secret keys `sk_j`; no inbound port; its only client is the ingest server | nothing checked, but it alone can decrypt (§10) |
| The tartuffe log | everything public, append-only | nothing: its consistency is checked |
| Bitcoin, via OpenTimestamps | hashes of log checkpoints | time ordering, to the hour |
| Ethereum RPC | the ZKPassport certificate registry, read by our server and by auditors | read-only |
| Aztec Ignition SRS | the universal KZG setup of UltraHonk, 176 participants, not us | soundness of the ballot proofs |
| Auditors (anyone) | archives of the relays, the log | — |

Adversaries:

- **A1. Our server, malicious:** drops, delays, injects, reorders ballots;
  invents voters; publishes wrong totals; shows different logs to different
  people; decrypts any single ballot; watches the relays live.
- **A2. A relay, malicious:** sees posting IPs and the filters readers send;
  drops, deletes, shows different sets to different readers.
- **A3. The State:** holds the chip data of every passport it issued, so it
  can compute any passport's ZKPassport identifier (§4); can compel one of the
  parties above.
- **A4. The public:** reads everything.
- **A5. A static host:** sees readers' IPs and fetch patterns; may serve
  altered files.

## 3. Notation and primitives

- `F_r`: the BN254 scalar field (the Noir field). `E`: Grumpkin
  (`y² = x³ − 17` over `F_r`), generator `G`, prime order `q` (the BN254 base
  field, `q > r`).
- `H(tag, x…)`: Noir stdlib `poseidon2::Poseidon2::hash(inputs, len)` over
  `F_r`, the domain tag first. Tags are the field elements of the ASCII
  strings `tartuffe-vote/2/<name>` (≤ 31 bytes) read as big-endian integers.
- Proof system: UltraHonk (Barretenberg 5.x), KZG on the Aztec Ignition SRS,
  verifier target `noir-recursive` (Poseidon2 transcript, zero-knowledge), so
  proofs can later be aggregated by recursion (§11).
- **Multi-recipient exponential ElGamal**: keys `PK_j = sk_j·G`, `j ∈ [0, J)`,
  independently generated, `sk_j` drawn below `r` (so it is one field
  element); one randomness `r_b` per ballot over the distinct keys:
  `C0 = r_b·G`, `C_j = p_j·G + r_b·PK_j`. Additively homomorphic per
  component. IND-CPA under DDH for independent keys.
- Log: RFC 6962 Merkle tree, leaf
  `"tartuffe-log/v1\n" ‖ seq ‖ "\n" ‖ kind ‖ "\n" ‖ body`, C2SP signed-note
  checkpoints (Ed25519), each checkpoint hash timestamped with
  OpenTimestamps.
- Bodies: canonical JSON (sorted keys, no spaces, integers as numbers,
  field elements and hashes as 0x-prefixed lowercase 32-byte hex), `"v": 2`
  in every body; points compressed (§6.4).
- `event`: the event's number, a field element; `slug`: its URL name.

## 4. Registration

1. The browser obtains the voter secret `s` (§4.1) **before** the scan, and
   confirms its backup (§4.2). It computes the leaf `ID = H(T_ID, event, s)`.
2. It opens the ZKPassport request: `age ≥ 18`, `nationality = FRA`,
   `document_type = passport`, `custom_data = H(T_REG, event, ID)`, domain =
   the app origin (§6.5), scope `tartuffe-vote/2:<slug>`, unique identifier
   type non-salted, proof mode `compressed` (one proof), **on-device proving**
   (no `cloudProverUrl`), `validity` 1 h. The age circuit also asserts the
   passport is not expired at the proof date.
3. It posts `register {v, event, id: ID, uid, proof, proof_date}` as a Nostr
   event to its registration relay (§7.1), `uid` the ZKPassport unique
   identifier for that domain and scope, `proof` the compressed proof and
   query result.
4. Our server, which watches the relays continuously, verifies the proof with
   the registration's `proof_date` as reference time, checks the rules,
   appends the `register` entry, inserts `ID` into the tree (§5), and posts a
   signed acknowledgement (§7.2) on the same relay: `{nostr_id, body_hash,
   log_seq, outcome: "logged", position}`. The next checkpoint (≤ 30 s, §8.2)
   carries a root that includes the voter; the browser then votes.
5. Rules: one `register` per `uid` per event, ever; one per `ID`. A
   registration seen on a relay and not logged is withholding (§12.6).
6. A voter who lost `s` and every backup keeps their last ballot but cannot
   register again: a second key would be a second vote, and ballots are
   unlinkable, so the old key's ballot cannot be cancelled.

What becomes public: that the passport with identifier `uid` registered for
this event, when (to the checkpoint), with leaf `ID`. The State can compute
`uid` from chip data it holds, so participation is public to it. Votes are
not (§10). Passports only, because a passport and a CNIe give two identifiers
(computed per document in zkpassport/circuits: `H(dg1, e_content, sod_sig)`);
a per-person identifier would need ZKPassport to add one.

### 4.1 The voter secret

Where the platform offers the WebAuthn PRF extension, the secret is derived
from the voter's passkey: `s = H(T_PRF, prf_lo, prf_hi)` with the 32-byte PRF
output of input `"tartuffe-vote/2/<slug>"` split in two 16-byte limbs. The
passkey is created before the scan (PRF output is usually available only at
assertion time: two biometric prompts). Apple and Google sync passkeys across
the voter's devices, so a lost phone does not lose the key; the passkey
provider learns that a passkey for the app origin exists, nothing else.
Otherwise `s` is random, stored with `navigator.storage.persist()`, and
backed up by 12 words or a device link (QR, end-to-end encrypted, through a
relay).

### 4.2 Backup before the first ballot

No ballot is cast before the backup is confirmed: a PRF passkey, or the 12
words typed back once (also offered to the password manager). Private
browsing, Tor Browser and in-app webviews are detected: the first two force
the 12 words (their storage is wiped), the webviews are sent to the system
browser (no passkeys, no threads). Safari deletes a site's storage after 7
days of use without a visit: with a PRF passkey nothing is lost; with a
random `s`, `persist()` and the backup cover it. The UI says before the scan:
« Si vous perdez cette clé, votre dernier bulletin reste compté et vous ne
pourrez plus le changer. »

## 5. The tree

- One append-only Merkle tree per event, fixed depth `D = 20` (to measure
  against a 3-ary tree of depth 13: one Poseidon2 permutation absorbs three
  inputs). Voter leaves `ID` at positions 0, 1, … in log order, empty leaves
  0, nodes `H(T_NODE, l, r)`; index bits read from the least significant bit
  at the leaf level. A leaf never changes.
- Every checkpoint (§8.2) carries the root of the leaves logged so far. A
  ballot proves membership against a **checkpoint root** only, at most
  `ANCHOR_AGE` (24 h) old when the ballot is logged, judged by checkpoint
  times. Never against a root the server gave only to one voter.
- The browser proves against the newest checkpoint root it has that contains
  its leaf.

### 5.1 Keeping the path up to date, privately

The browser never asks for its own path, its own leaf or its own block: that
would tie its IP to its `ID`.

- The mirror publishes, for everyone alike: the leaves added per **UTC day**
  (`leaves/<day>`), the root of every full block of 256 leaves
  (`blocks/roots`), the leaves of every full block in groups of 16
  (`blocks/<group>`), and the block being filled (`blocks/open`).
- At registration the browser knows its position (§4, step 4) and computes
  its path from `blocks/open` plus the newest `register` events it reads on
  its sticky relay (§7.1) with the same filter every browser sends.
- Once its block is full, the browser caches its 8 intra-block siblings for
  good. Afterwards each visit downloads only `blocks/roots` (125 KB at a
  million voters, the same file for everyone) and `blocks/open`, and recomputes
  the 12 upper levels.
- A device restored from a backup finds its position without naming it: it
  downloads the shard `positions/<prefix>` of the `ID → position` table for
  the first 4 bits of `ID` (1/16 of the leaves), then its block's fixed group
  `blocks/<position div 4096>`. The group is fixed by the position, so repeated
  visits intersect to nothing new.
- The recomputed root is checked against the newest checkpoint root; a
  mismatch means a bad mirror or relay, and the browser tries another source.

## 6. The ballot

### 6.1 Options and values

- The event has `N` option slots (`N = 10`): candidates, then **blank** at
  index `B`, the rest **reserved**. A reserved slot can be ranked only once a
  setup names a candidate for it (§11); older ballots then count it as
  unranked, which is what they meant.
- A ballot ranks `len ∈ {0,1,2,3}` distinct allowed options `o_0…o_{len−1}`;
  `len = 0` is a **withdrawal**. Blank only last.
- `pos(c)`: the rank of `c`, or 3 if unranked (an unranked option comes after
  every ranked one, blank included).
- Values, each in `{−1, 0, 1}`, in this order, `V = 1 + 2N + N(N−1)/2 = 66`:

| Values | Meaning |
|---|---|
| `voter` | 1 if `len ≥ 1` |
| `first[c]`, `c = 0…N−1` | 1 if `c = o_0` |
| `ranked[c]` | 1 if `pos(c) < 3` |
| `margin[i<j]`, `(i, j)` lexicographic | +1 if `pos(i) < pos(j)`, −1 if `pos(j) < pos(i)`, else 0 |

- **Packing.** Each value is shifted to `v + 1 ∈ {0, 1, 2}` and 7 values share
  one plaintext as base-`2^L` digits, `L = 36`:
  `p_j = Σ_{k<7} (v_{7j+k} + 1) · 2^{Lk}`, the first value in the lowest
  digit. `J = ⌈66/7⌉ = 10` ciphertexts; `7·36 = 252 < log2 r`. A sum over `c`
  ballots decodes digit by digit while `2c < 2^36` (34 billion ballots), then
  subtracts `c` from each digit.

### 6.2 The ballot statement (one UltraHonk proof)

Public inputs, in this order: `event`, `setup` (log seq of the setup the
ballot was made under), `mask` (the allowed options of that setup, bit `c`
set for option `c`), `root`, `nullifier`, `seq`, `C0.x, C0.y`,
`C_0.x, C_0.y, … C_9.x, C_9.y`.

Private inputs: `s`, the Merkle path (`D` siblings, `D` direction bits), the
ranking `(len, o_0, o_1, o_2)`, `r_b`.

The circuit asserts:

1. **Membership:** `H(T_ID, event, s)` is a leaf of the tree with root `root`.
2. **Pseudonym:** `nullifier = H(T_NULL, event, s)`.
3. **Sequence:** `seq < 2^32`.
4. **Ranking:** `len ≤ 3`; for `k < len`: `o_k < N`, bit `o_k` of `mask` set,
   `o_k` distinct, `o_k = B ⇒ k = len − 1`; for `k ≥ len`: `o_k = 0`.
5. **Values:** computed from the ranking as in §6.1 and packed as in §6.1.
6. **Encryption:** `r_b ≠ 0`, `C0 = r_b·G`, `C_j = p_j·G + r_b·PK_j`, with the
   `PK_j` compiled into the circuit (one circuit per election key set); no
   output is the point at infinity.
7. **Binding:** `event, setup, mask, seq` are public inputs, so no relay or
   server can change them.

The verifier takes `mask` and the verification key from the named setup,
never from the ballot. One proof per ballot: no Semaphore, no Groth16, no
ceremony of ours.

**Phone cost contingency.** If the measured proof exceeds about 5 s on a
2021 mid-range Android (Q2), the statement splits as in MACI: the phone proves
1–3 and binds a hash of the ciphertexts; our tallier, which decrypts every
ballot anyway, proves 4–6 for each logged ballot in a batch proof appended
to the log. P2 and P3 are unchanged; what moves is where the content proof
is computed.

### 6.3 Which ballot counts

- For each pseudonym, the **latest valid ballot** counts: the highest `seq`.
  A second ballot with an already logged `(nullifier, seq)` is rejected
  (`stale_seq`); the first logged wins. Duplicates are judged by the
  **public-input hash** `H_pi = SHA-256(canonical public inputs)`, never by
  proof bytes.
- A restored device learns its last `seq` from the public index
  `index/<prefix>`: the `nullifier → highest logged seq` table sharded by the
  first 8 bits of the nullifier. The browser fetches a fixed set of 4 shards
  derived from `s`, its own among them, on every visit that needs it.

### 6.4 Encoding

`ballot {v, event, setup, root, nullifier, seq, c0, c[J], proof}`. Points
compressed as 32 bytes: `x` with the parity of `y` in the top bit; decoders
reject non-canonical encodings, points off the curve, and the point at
infinity. The proof is base64. About 11 × 33 B + 15 KB ≈ 16 KB; the proof is
the bulk and its size does not depend on the circuit's size.

### 6.5 Waiting time

- **When the voter taps « Voter »**, before the scan, the browser downloads
  from the data mirror the circuit, its verification key and the SRS part it
  needs (about 4–8 MB, 6 s on 4G, once; their hashes are in the setup), and
  starts the prover (WebAssembly, threads). They are kept in Cache Storage.
  A returning registered voter's page warms up on load, together with the
  path update (§5.1). The registration flow is resumable: iOS suspends the
  tab while the ZKPassport app runs, so the warm-up restarts on return.
- The app origin is `<org>.github.io` (Q3): the ZKPassport domain and the
  passkey RP ID are bound to it for the life of the event, and nobody can
  repoint it. GitHub Pages cannot send `Cross-Origin-Opener-Policy` and
  `Cross-Origin-Embedder-Policy`, so the app ships a service-worker shim
  (coi-serviceworker) that adds them; the first visit reloads once. Without
  isolation the prover runs on one thread (3.1 s on an M-series Mac, 8–15 s
  on a mid-range phone: the measured fallback).
- The ranking is persisted before proving; a crash offers « reprendre ».
  Proof progress is a determinate bar.
- Measured on the draft-1 prototype (44 values, 33 ciphertexts' worth of
  scalar work, 50.6k gates): 0.39 s native, 0.8 s in Chrome with 8 threads,
  3.1 s with one thread, proof 14.6 KB, verify 10 ms native. The 10-ciphertext
  circuit should stay well under 2^16 gates; phones to measure (Q2).

## 7. Transport

### 7.1 Relays

- Each device picks **one sticky relay** for ballots, derived from `s` and the
  setup's relay list (`H(T_RELAY, s, …)` mod the number of eligible relays),
  and keeps it; it changes only when that relay fails, to the next in the
  derived order. Rotating would spread the IP ↔ pseudonym link to every relay
  used; sticking exposes it to one.
- The **registration relay** is a different relay, derived the same way, and
  it never receives a ballot from this key: the registration shows `uid` with
  the IP, the ballots show the pseudonym with the IP.
- One websocket per event posted; subscriptions for acknowledgements use the
  same filter every browser sends (`kinds`, the event tag, `since` = the last
  checkpoint time), never the voter's own event id.
- **There is no direct path to our server.** If the sticky relay refuses, the
  browser tries the next; if none accepts, the page says so and retries later,
  keeping the ballot. It never sends the ballot to us.
- Nostr: regular event kinds (ballot 1_7020, register 1_7021, ack 1_7022,
  checkpoint 1_7023, complaint 1_7024, snapshot 1_7025), tag
  `t = tartuffe-vote/2/<slug>`, each voter event signed by a fresh throwaway
  key, `created_at` rounded down to the minute (the hour would make paging
  stall: all events of an hour share one timestamp). Catch-up uses NIP-77
  (negentropy) where the relay supports it, paging by `until` otherwise.
- Relay requirements (checked at setup time from NIP-11): `max_message_length`
  ≥ 32 KB, no payment, no proof-of-work, no auth, persistent storage
  (strfry-class), retention ≥ 30 days, different operators. A relay can be
  marked **retired** by a later setup: browsers stop posting there, auditors
  keep reading it.

### 7.2 Acknowledgements and complaints

- For every event it ingests, our server posts on the relay it read it from a
  signed `ack {nostr_id, body_hash, pi_hash, log_seq, outcome, reason?}`,
  `outcome ∈ {logged, rejected}`. The browser treats a `duplicate` ack whose
  `body_hash` matches its own repost as success. On `old_root`, `stale_seq` or
  `unknown_setup` it re-proves with fresh values and reposts, then tells the
  voter only if that fails.
- A signed ack with `outcome: logged` whose `log_seq` is not that entry in a
  later checkpoint is self-incriminating.
- On its next visit (or at the next checkpoint while the page is open) the
  browser checks inclusion privately (§12). If the ballot is missing, it
  reposts to two other relays and posts the signed ack as a `complaint`
  event; auditors collect complaints.
- With no ack within 20 s the browser reposts to the next relay of its derived
  order.

### 7.3 Our server's traffic

Our server posts acks, checkpoints and the close snapshot. It posts no
ballots and no dummies: dummies add zero to every total, so they cannot hide
what a results update reveals (§9.1), and a relay recognises our server's IP
anyway.

### 7.4 Flooding

Ballots are bounded by registrations (one pseudonym per passport; a flood
from one key only replaces its own ballot). Relays apply their own rate
limits. Our server verifies every ballot it reads; events carrying the tag
that do not parse as a protocol body are logged as a hash only (§8.1).

## 8. The log

### 8.1 Ingestion

Our server subscribes to every relay of the setup, continuously, and catches
up after any gap (§7.1). For each new event of a protocol kind it checks
structure, setup, root (a checkpoint root ≤ `ANCHOR_AGE` old), the proof and
the `seq` rule, then appends:

- `ballot` (the ballot in full) or `register`, or
- `reject {v, event, nostr_id, body, reason}` with the body in full (capped
  at the ballot size) and a reason from a closed list (`malformed`,
  `unknown_setup`, `old_root`, `bad_proof`, `duplicate`, `stale_seq`,
  `closed`), each re-checkable by anyone from the stored body and the log;
- `seen {v, nostr_id, hash}` for a tagged event that is not a protocol body.

Every event id carrying the tag is thus listed, including copies of an
already logged body (`duplicate`), so an auditor never finds an unlisted
event. Entries ingested between two checkpoints are ordered by `pi_hash`, not
by arrival.

### 8.2 Checkpoints

Whenever entries were added, and at most every `CHECKPOINT_PERIOD` (20 s),
our server signs a checkpoint whose note carries the tree root (§5), the
mirror commit hash (§8.3) and the server time; timestamps it with
OpenTimestamps; posts it to every relay of the setup; and pushes the mirror.
Nothing is published when nothing happened.

### 8.3 The data mirror

A public GitHub repository, read through jsDelivr **by the commit hash named
in the checkpoint** (branch URLs are cached 12 h; GitHub Pages is never the
data host), holds what browsers read: checkpoints and tallies, the tree files
of §5.1, the index shards of §6.3, and per-checkpoint entry files
`entries/<checkpoint>` listing `(pi_hash, log_seq)` of every entry, from which
a browser finds its receipt without fetching its own entry. Every file is
immutable and under 20 MB; the repository rotates (the checkpoint names it)
before 150 MB. The full log, proofs included, stays on our server for auditors
(an auditor reveals nothing by downloading it all) and may be mirrored by
anyone.

Browsers reach data through one **sources** module: an ordered list of
mirrors, fetch by path under a commit hash, check the signature and the chain
to a checkpoint, fall back to the next mirror on failure. Other mirrors (a
second forge, IPFS with a pinning service) are entries in that list.

## 9. Tally, results, close

### 9.1 Tallies: the `K_REAL` rule

The difference between two consecutive results is exactly the sum of the
ballots that changed in between, whatever else surrounds them. An update that
holds one changed ballot publishes that ballot's full ranking. So:

**A tally is published only when at least `K_REAL` (10) distinct pseudonyms
have changed their decrypted values since the previous tally** (a first
ballot and a withdrawal count as changes; a recast identical to the previous
ballot does not). There is no time cap: a cap would be the leak. The tallier
counts changes from its own per-ballot decryptions; the public cannot check
`K_REAL` and does not need to, since it protects voters from the public and
the relays, and the tallier holds `sk` anyway. Auditors check the weak public
bound `‖Δfirst‖₁ ≤ 2 × (ballots since the previous tally)`.

When the rule fires, the tallier:

1. takes, for every pseudonym, its latest valid ballot logged up to `upto`;
2. sums them per component: `S0 = Σ C0`, `S_j = Σ C_j`, and counts them, `c`;
3. knows each ballot's `p_j` from its own per-ballot decryption (a table over
   the ≈ 820 valid rankings), so `P_j = Σ p_j` is known exactly and checked by
   `S_j − sk_j·S0 = P_j·G`: no discrete log of a sum;
4. unpacks each `P_j` into 7 digits, checks `0 ≤ digit ≤ 2c`, subtracts `c`;
5. appends `tally {v, event, upto, c, s0, s[J], p[J], dleq[J]}` where `dleq[j]`
   is a Chaum-Pedersen proof that `log_G PK_j = log_{S0}(S_j − P_j·G)`:
   nonce `k = H(T_NONCE, sk_j, event, upto, j, S0.x, S0.y, S_j.x, S_j.y, P_j)`,
   commitments `A = k·G`, `B = k·S0`, challenge
   `e = H(T_DLEQ, event, upto, j, G.x, G.y, PK_j.x, PK_j.y, S0.x, S0.y, S_j.x, S_j.y, P_j, A.x, A.y, B.x, B.y)`,
   response `z = k + e·sk_j mod q` (the challenge in `F_r` used mod `q`:
   statistical gap below 2^−127). Verifiers recompute `S0, S` from the log,
   never from the tally body.

**Everything shown or published after decryption comes from the decrypted
values alone**: voters, first choices, ranked, duels. What the page shows
live between tallies comes only from public log data: the number of ballots
logged, the number since the last tally, the voter's own receipt. Never a
count of real changes (that would say whether a recast changed anything).

Results: voters = `voter` total; first choices; ranked; Schulze on the
margins `d[i][j] − d[j][i]` (the margin variant), over the named options and
blank; equal strengths give equal ranks; the Condorcet winner is the option
that strictly beats every other one, if there is one.

**What the voter sees.** Receipt within seconds (the ack), inclusion within a
checkpoint (≤ 20 s plus the mirror fetch), « 1 247 bulletins reçus · 13 depuis
la dernière mise à jour · prochaine mise à jour dès 10 bulletins modifiés »,
their own ranking shown locally. At busy times (launch, debates) the rule
fires every minute or so; at a vote every 5 minutes the median wait is about
25 minutes; at night, the morning. The daily variant (« chaque soir à 20 h,
par lots d'au moins 10 bulletins modifiés, sinon reporté ») is the same rule
with a minimum gap of 24 h, a configuration of the setup (`tally_min_gap`).

### 9.2 Blackouts

During a legal blackout (the day before and the day of a round, until 20:00
Paris time), ballots are still logged (they are encrypted) but no tally is
appended; the first tally after the blackout covers everything. The tallier
still decrypts during the blackout: our server can read the trend; the public
cannot.

### 9.3 Close

- `closes` is the end of the final blackout (20:00 Paris time on the day of
  the round), so the final tally covers at least a day of changes; if fewer
  than `K_REAL` ballots changed in that day the final difference is small:
  stated residual.
- The page stops accepting new ballots at `T − 10 min` and says so before
  proving; a send in flight completes.
- At `T` our server signs the **snapshot**: the Merkle root over the sorted
  ids of every protocol event it has seen, posted to every relay (auditors
  reject a snapshot first seen after `T + 2 min`), with the list on the
  mirror. Auditors take their own snapshots: events they archived before
  `T − 5 min` must be in it; events in `[T − 5 min, T]` may or may not be.
- It ingests exactly the snapshot's events, judging validity with time = `T`,
  appends the final `tally`, then the **final shuffle** (§9.4), then
  `closed {v, event, at, snapshot_root, final_tally, shuffle}`. Events not in
  the snapshot are rejected with `closed`. After `closed` nothing is accepted
  and the final result is shown for good.
- 30 days after `closed`, the tallier destroys `sk` and its shares and our
  server appends `destroyed {v, event, at}`.

### 9.4 The final shuffle

With the final tally, our tallier takes the latest ballot of every pseudonym,
re-encrypts and permutes them with a Bayer–Groth shuffle proof (commitment
keys derived from a public nothing-up-my-sleeve hash of the setup), and
decrypts each output with a DLEQ proof. The log then holds every final
ranking in clear, unlinkable to pseudonyms for the public. Anyone recomputes
Schulze, first choices or any other method from the ballots themselves; the
margins tally is the cross-check. The tallier knows the permutation, as it
knows `sk`.

## 10. What each party learns

| Observer | IP of a ballot | Pseudonym | Vote | Passport of a pseudonym |
|---|---|---|---|---|
| The sticky relay of a device | yes, for that device's ballots | yes | no | no |
| The registration relay of a device | yes, for the registration | no | no | sees `uid` with the IP (the State can name the passport) |
| Two relays pooling (a device's sticky and registration relays) | yes | yes | no | yes, by IP |
| A static host (app or mirror) | readers' IPs; the same files for all, cover groups fixed per device | no | no | no |
| ZKPassport bridge | the phone's IP at registration | no | no | no (`uid` stays on the phone and in the relay post) |
| Passkey provider | — | no | no | learns a passkey for the app origin exists |
| Our server (ingest) | **no**: no voter request reaches it | yes | no | no, except the first-ballot timing below |
| Our tallier | no | yes | **yes: it holds `sk`** | no, except the first-ballot timing below |
| The public | no | yes | no: only sums of ≥ `K_REAL` changed ballots | no, except the first-ballot timing |
| The State, alone | no | yes | no | can compute `uid → ID`, so participation; `ID → pseudonym` only by the first-ballot timing |
| Our tallier and one relay | for that relay's ballots | yes | yes | — |
| Every relay pooling with our tallier | yes | yes | yes | — : Tor Browser covers this |
| A global passive observer (ISP level) | yes | by timing against the relay post | no | — : Tor Browser |

- **Updates.** A tally's difference from the previous one is the sum of at
  least `K_REAL` changed rankings. A relay that carried one of them learns
  that its ballot is one of ≥ 10 whose sum is public; a sum of 10 rankings
  often splits into its parts, so the protection is the 1-in-10
  attribution, not the secrecy of the rankings in the batch. A group holding
  `K_REAL − 1` passports could fill a batch around one target's change:
  stated residual (raising `K_REAL` is the only lever; the owner chose 10 for
  the latency of results, 2026-10-26).
- **First ballot.** A new pseudonym's first ballot follows its registration;
  both are dated to a checkpoint. On a busy day the anonymity set is the
  registrants of that checkpoint; at quiet times it is one. That ties a
  passport to a pseudonym for the State and the public, never to a vote: the
  vote is in a sum of ≥ `K_REAL`. Accepted: no wait before a first vote.
- **The app.** A host serving altered JavaScript to a target defeats every
  check in the browser. Mitigation: a reproducible build whose hash is in the
  setup, served from an origin we cannot repoint; auditors and a browser
  extension compare.
- **The key.** `sk` decrypts any single ballot, which is forced by live
  results without trustees; threshold trustees (Belenios-style) are the
  upgrade path if the consultation ever becomes more than one. `sk` lives on
  the tallier, which has no inbound port; it is generated at event creation,
  backed up as a 3-of-5 Shamir split held offline, and destroyed 30 days after
  the close. A later leak, or a quantum computer, could decrypt individual
  ballots: they stay pseudonymous (`ID` and the pseudonyms are Poseidon2
  hashes behind a zero-knowledge proof).
- **Tor Browser** hides the IP from relays, hosts and the ISP; it runs the
  prover on one thread (no `SharedArrayBuffer`), only at the « Standard »
  level (WebAssembly is off above it), with the 12 words at every visit
  (storage is wiped) and no platform passkey. The phone's IP still reaches
  the ZKPassport bridge during the scan.

## 11. Versioning and migration

- Every body carries `"v"`. A verifier rejects a version it does not know.
- `setup {v, event, version, options:[…], blank, mask, n_cap, pk:[…],
  pk_pok:[…], vk_hash, circuit:{noir, bb, source_hash}, app_hash, srs_hash,
  relays:[{url, state}], k_real, tally_min_gap, anchor_age, checkpoint_period,
  closes, blackouts:[…]}`. `pk_pok[j]` is a Schnorr proof of knowledge of
  `sk_j` with challenge `H(T_POK, event, j, PK_j.x, PK_j.y, A.x, A.y)`.
- **Fixed for the event:** `n_cap`, `blank`, the keys, `vk_hash`, `srs_hash`,
  `k_real`, `anchor_age`, `closes`, `blackouts`. Relays may be added or
  retired, never removed. `tally_min_gap` may only grow.
- **A later setup** may only name a reserved slot, withdraw a candidate (shown
  as withdrawn, dropped from the duels, its first choices shown apart; a
  withdrawn slot is never renamed), add or retire relays, change `app_hash`,
  `checkpoint_period` or `tally_min_gap`. It takes effect 24 h after it is
  logged; a ballot naming a setup not yet in effect is rejected
  `unknown_setup`. A ballot made under any setup of the event stays valid and
  keeps counting.
- **A soundness bug in the circuit** or any change to a fixed field means a
  new event: the old one gets an `abort {v, event, at, reason, last_tally}`
  entry and is frozen as a record.
- **Aggregation, later:** an optional `aggregate {v, event, upto, proof}`
  entry proving by recursion that every ballot up to `upto` verifies and that
  `S0, S, c` are their sums. Verifiers that know it may skip the per-ballot
  proofs; others ignore it.
- From tartuffe-vote/1: nothing is in production; the launch starts a fresh
  log.

## 12. Public verification

An auditor (`dm poll verify`, open source, incremental per checkpoint) checks,
from the log, the relays and its own continuous archive of them:

1. **Log:** every checkpoint signed by the published key, each consistent
   with the previous (RFC 6962), its hash in Bitcoin; the checkpoints seen on
   at least two relays and on the mirror are one chain (no split view).
2. **Setup:** keys on the curve, distinct, not the identity nor `±G`;
   `pk_pok` valid; the `PK_j` compiled into the circuit equal to `setup.pk`;
   `vk_hash` equal to the key built from the published source with the pinned
   `nargo` and `bb`; `app_hash` equal to the reproducible build; later setups
   within §11.
3. **Registrations:** every ZKPassport proof verifies locally with the
   registration's `proof_date` as the reference time (the stock verifier
   compares with today and would fail weeks later), with the certificate
   registry root valid at that date (Ethereum RPC): age, nationality,
   passport, not expired, `custom_data = H(T_REG, event, ID)`, domain and
   scope; `uid` and `ID` unique.
4. **Tree:** every checkpoint root is the root of the leaves logged so far.
5. **Ballots:** every `ballot` verifies against its setup, a checkpoint root
   at most `anchor_age` older than its checkpoint, and the `seq` rule; every
   `reject` deserves its reason, judged against the auditor's own first
   sighting of the event.
6. **Withholding (P1):** every protocol event the auditor archived at time
   `t` is listed by its body in every checkpoint the auditor first saw on a
   relay at or after `t + 10 min`: as `ballot`, `register`, `reject` or
   `seen`. A body seen anywhere and never logged is withholding; an unlisted
   Nostr id whose body is logged is a relay-copy discrepancy, reported but not
   withholding. Complaints (§7.2) are checked the same way.
7. **Tallies:** the latest valid ballot per pseudonym up to `upto`, their
   count `c`, their sums equal to `s0, s`, every digit within `[0, 2c]`, every
   DLEQ proof valid, the public bound of §9.1, `tally_min_gap` respected, no
   tally inside a blackout, and the results shown computed from the decrypted
   values.
8. **Close:** the snapshot first seen on the relays by `T + 2 min`, the final
   tally covering it, the shuffle proof and the output decryptions valid, the
   plaintext rankings consistent with the final tally, nothing accepted after
   `closed`, `destroyed` logged.

The voter's browser checks for itself: the ack of its ballot (outcome
`logged`); its `pi_hash` in the `entries/<checkpoint>` files it fetches for
the checkpoints since its post, the same files every visitor fetches;
checkpoints that only grow; the latest tally's DLEQ proofs (which prove the
decryption of the sums the log holds). On request, the full sum (every
ballot's ciphertexts, about 360 B each).

Cost at a million voters, about 8 million ballots: about 22 CPU-hours of
proof verification and a 100 GB log for a full audit, incremental afterwards;
`aggregate` (§11) is the planned relief.

## 13. Constants and parameters

| Name | Value | Why |
|---|---|---|
| `N` | 10 | 7 candidates, blank, 2 reserved |
| `V`, `J`, `L` | 66, 10, 36 | values, ciphertexts, bits per digit |
| `D` | 20 | a million leaves |
| `K_REAL` | 10 | changed ballots per tally: the anonymity set of an update |
| `ANCHOR_AGE` | 24 h | how old a ballot's root may be |
| `CHECKPOINT_PERIOD` | 20 s | receipt latency |
| `tally_min_gap` | 0 (live) or 24 h (daily) | the owner's choice of rhythm |
| relays | ≥ 5 persistent, different operators, ≥ 32 KB messages | §7.1 |

Domain tags: `T_ID`, `T_NULL`, `T_NODE`, `T_REG`, `T_PRF`, `T_RELAY`,
`T_DLEQ`, `T_NONCE`, `T_POK`. Nostr kinds in §7.1.

## 14. Before implementation

Formats to fix with the circuit, and test vectors to publish with it:

- the exact Poseidon2 call for every `H` (arity, the stdlib function), the
  tag → field rule, the `EmbeddedCurvePoint {x, y, is_infinite}` layout of
  public inputs, the `Field → EmbeddedCurveScalar` (lo/hi 128) conversion for
  `r_b` and `p_j`, the packing order, the tree index bit order, the Nostr
  event JSON of every kind, the ack, complaint and snapshot bodies, the
  canonical JSON rule, `pi_hash` and the snapshot root;
- vectors: `ID` and nullifier for a given `(event, s)`; a depth-20 root with 3
  leaves; `p_j` for five rankings (blank only; `A, B, blank`; a withdrawal; a
  full 3-ranking; a reserved-slot reject); one complete ballot (public inputs,
  proof, `vk_hash`); a sum of 3 ballots with `c`, the digits and the results;
  one DLEQ with its nonce and challenge; one `pk_pok`; Schulze margins on a
  4-option matrix with a tie and a blank Condorcet win; a checkpoint, a
  consistency proof and an OTS stub; one negative vector per reject reason.

Open questions:

- **Q1.** Confirm with ZKPassport that the `domain` is authenticated against
  the web origin (else the phishing residual stands as written), and that
  `compressed` mode proves on the device without a cloud prover.
- **Q2.** Measure the 10-ciphertext circuit on an iPhone 12-class Safari and a
  Pixel 6-class Chrome, with memory, isolated and single-threaded; decide the
  contingency of §6.2.
- **Q3.** The app origin: a GitHub organisation `tartuffe-vote`
  (`tartuffe-vote.github.io`), fixed for the event, versus `vote.tartuffe.me`,
  which we could repoint. The spec assumes the former.
- **Q4.** Relay recruitment: measure five candidate relays against §7.1 and
  name two fallbacks.
