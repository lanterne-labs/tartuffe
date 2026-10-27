# tartuffe-vote/2: encrypted ranked ballots with a public homomorphic tally

Status: **draft 4, reviewed; implemented and run locally end to end**, 2026-10-26. §15 says what is built and what is not.
Nothing here is implemented yet. Drafts 1, 2 and 3 were each reviewed on
2026-10-26 (adversarial protocol review ×3, a study of alternatives, a
privacy and UX study); draft 4 closes the final review's blocking items
(registration freshness, the acknowledgement body, the relay ranking, the
P1 residual) and its recommendations. It replaces tartuffe-vote/1
(plain anonymous ballots in 30-day windows, `backend/polls`), which never ran
in production.

## 1. Goals

The event is a public, continuous consultation (not an election, not a
sondage). French adults register once with their passport, rank up to three
candidates or vote blank, may change their ballot until the close, and
everyone sees the live results.

Required properties, against the adversaries of §2:

- **P1. Public detection of withholding.** Any third party can detect, from
  the relays and the log alone and at any later time, that a ballot a voter
  cast is missing from the count: every event on a relay must carry our
  server's signed acknowledgement naming the checkpoint that lists it
  (§7.2). An event never acknowledged stays on the relays as a public
  finding; attributing it (our server withheld it, or the relay never
  delivered it) takes a witness: an auditor's continuous archive or the
  relay's operator (§12.6).
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
- **P1 under our server colluding with a voter's own sticky relay** rests on
  that voter's browser: the relay can show the ballot and the acknowledgement
  to that voter alone, and only the browser's inclusion check (§7.2, within
  20 s while the page is open, and at every later visit) turns the loss into a
  public complaint. A voter who closes the page before « vérifié » and never
  returns has no third party watching. Any other relay, and the honest-relay
  case, are covered by auditors;
- **attribution of an unacknowledged event** (P1): an event on a relay with
  no acknowledgement and no logged body is detected by anyone, post hoc,
  but our server can claim the relay never delivered it. The browser's
  reposts (an unacknowledged ballot is reposted to two more relays, then
  shown as failed to the voter) make a withholding server leave that trail
  on several relays; a witness who saw the event on the relay in time (an
  auditor's archive, the relay's operator) settles it;
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
4. Our server, which watches the relays continuously, checks **freshness**
   first: `|proof_date − now| ≤ validity + 5 min`, else `reject stale_date`
   (without this bound a phone with its clock set to 2033 proves "adult" for
   a minor and "not expired" for an expired passport). It then verifies the
   proof with `proof_date` as the reference date (the SDK's own check is only
   `|date − now| ≤ validity`; the verifier is told the proof's date, and the
   identifier type is pinned to the document's own, non-salted one), checks
   the rules, records the registration as pending in its store, and posts a
   signed acknowledgement (§7.2) on the same relay. The next checkpoint
   (≤ 20 s, §8.2) logs the registration (with the other entries of the
   checkpoint, in one transaction), appends `ID` to the tree (§5) and
   carries a root that includes the voter; the browser finds its position among that
   checkpoint's new leaves (a file everyone fetches, §8.3) and then votes.
   The acknowledgement carries no position: positions follow the log order
   of the checkpoint, which is by hash (§8.1), not by arrival. The browser
   waits up to 60 s for the acknowledgement (verification queues at launch)
   before reposting to its next relay.
5. Rules: one `register` per `uid` per event, ever (`reject uid_used`); one
   per `ID` (`reject id_used`); `custom_data` compared as lower-case hex. A
   registration on a relay, acknowledged and not logged, is withholding
   (§12.6).
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
backed up by 12 words or a device link: the new device shows a URL (as a QR
code and as text) carrying an ephemeral secp256k1 key and listens on every
relay of the setup; the device that holds `s` opens it, the voter confirms,
and it posts `{s, position, seq}` sealed for that key (ECDH,
XChaCha20-Poly1305) as a `link` event (kind 7026, a second `t` tag
`link:<receiver key>`) to the first of its relays that takes it. Our server never sees it; the relay sees a ciphertext and the two
devices' addresses. The 12 words are offered to PRF users too: a passkey can be deleted
or a provider account lost, and the words are the only backup that depends
on nobody.

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
  its leaf, with one rule for a **first ballot**: it uses the newest
  checkpoint root (within `ANCHOR_AGE`) in which its leaf is **not among the
  last `R_MIN` = 32 leaves**, if such a root exists, else the newest root.
  Otherwise a server could isolate one registration in a checkpoint of its
  own at a busy time and shrink that voter's anonymity set to one; with the
  rule, a first ballot at busy times proves against a root that at least 32
  later registrants share, and at quiet times the residual of §1 applies.

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
  `blocks/<position div 4096>`. The group is fixed by the position, so
  repeated visits intersect to nothing new; the host learns the intersection
  of the shard and the group, about 256 leaves.
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
  digit; the 4 unused digits of `p_9` hold the value 0, i.e. the digit 1, so
  every digit decodes alike. `J = ⌈66/7⌉ = 10` ciphertexts;
  `7·36 = 252 < log2 r`. A sum over `c` ballots decodes digit by digit while
  `2c < 2^36` (34 billion ballots), then subtracts `c` from each digit.

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
1–3 and binds `H(T_CT, C0, C_0…C_9)`; our tallier proves, with `sk_j` as its
witness (`PK_j = sk_j·G` and a valid packing `p` with
`C_j − sk_j·C0 = p_j·G`, the on-curve checks moving to the verifier), a batch
proof over each checkpoint's ballots that outputs one validity bit per
ballot; only ballots proven valid count, an invalid one leaves the voter's
previous ballot in force, and the tally waits for the batch proof. P2 and P3
are unchanged. This variant is specified in full only if Q2 triggers it.

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

- Each device ranks the relays of the setup in force at its registration by
  **rendezvous hashing**: score `H(T_RELAY, s, relay_url)` per relay, sorted
  ascending. Rank 0 is the **registration relay**; it never receives a ballot
  from this key (the registration shows `uid` with the IP, the ballots show
  the pseudonym with the IP). The **sticky relay** for ballots is the
  best-ranked other relay that is not retired; on failure the device moves to
  the next in its ranking and never returns to a recovered relay. Relays added
  by a later setup are ranked by new devices only. Rotating would spread the
  IP ↔ pseudonym link to every relay used; sticking exposes it to one, and a
  rank-based rule keeps every device's choice stable when the list changes.
- One websocket per event posted; subscriptions for acknowledgements use the
  same filter every browser sends (`kinds`, the event tag, `since` = the last
  checkpoint time), never the voter's own event id.
- **There is no direct path to our server.** If the sticky relay refuses, the
  browser tries the next; if none accepts, the page says so and retries later,
  keeping the ballot. It never sends the ballot to us.
- Nostr: regular event kinds (ballot 7020, register 7021, ack 7022,
  checkpoint 7023, complaint 7024, snapshot 7025: kinds 10000–19999 are
  replaceable, one event per key and kind, which would keep one ack per
  relay), tag
  `t = tartuffe-vote/2/<slug>`, each voter event signed by a fresh throwaway
  key, `created_at` rounded down to the minute (the hour would make paging
  stall: all events of an hour share one timestamp). Our server's events
  (acks, checkpoints, snapshot) are signed by its Nostr key (secp256k1),
  named in the setup, distinct from the Ed25519 log key. Catch-up uses NIP-77
  (negentropy) where the relay supports it, paging by `until` otherwise.
- Relay requirements (checked at setup time from NIP-11 and measured by
  `dm vote relays probe`, which posts a ballot-sized event of each kind and
  reads back the earlier runs' events: a relay's real retention is what
  the post-hoc check of §12.6 rests on): `max_message_length` ≥ 32 KB, no
  payment, no proof-of-work, no auth, no web-of-trust or NIP-05 gate (the
  voters' keys are throwaway), persistent storage (strfry-class), retention
  ≥ 30 days, different operators. First probe (2026-10-27): of 22 public
  relays, 8 took both kinds (nos.lol, relay.primal.net, nostr.mom,
  nostr.oxtr.dev, relay.nostr.net, nostr-pub.wellorder.net,
  relay.nostr.wirednet.jp, nostr.sathoarder.com); the others gate writes
  (web of trust, NIP-05, payment) or were down; retention to be read over
  the following weeks (`docs/vote/relays-probe.json`). A relay can be
  marked **retired** by a later setup: browsers stop posting there, auditors
  keep reading it.

### 7.2 Acknowledgements and complaints

- For every event it ingests, our server posts on the relay it read it from,
  within seconds, a signed `ack {nostr_id, body_hash, pi_hash, checkpoint,
  outcome, reason?, position?}`, `outcome ∈ {logged, rejected}`,
  `checkpoint` the index of the checkpoint that will hold the entry (the log
  seq is not known yet: entries of a checkpoint are ordered by `pi_hash` when
  it closes). **Either outcome is self-incriminating** if that checkpoint's
  entries do not list `pi_hash` (as `ballot`, `register` or `reject`).
- **The acknowledgement is the obligation.** Every event of a protocol kind
  on an active relay, tagged for the event, gets its acknowledgement on that
  relay before the next checkpoint. The relays keep both (retention ≥ 30
  days, §7.1); the acknowledgement, not an auditor's presence at the time,
  is what makes withholding provable later (§12.6).
- **Our server keeps its promise through a restart.** It writes the entry
  as pending in its store before it posts the acknowledgement, and a server
  that starts again logs every pending entry at the checkpoint the
  acknowledgements named (the next one). A failure of its own, the database
  or a verifier process, never becomes an outcome: the event waits for the
  next tick and is judged then (§8.1). An acknowledgement a relay refused
  is posted again at each tick for an hour; its content does not change.
- The browser treats a `duplicate` ack whose `body_hash` matches its own
  repost as success. On `old_root`, `stale_seq` or `unknown_setup` it
  re-proves with fresh values and reposts, then tells the voter only if that
  fails. On `bad_proof` it re-verifies its own proof locally; if the proof
  verifies, the reject is false and it complains (below).
- The page shows « enregistré » at the ack and **« vérifié »** once `pi_hash`
  appears in the entries of the named checkpoint (≤ 20 s, read from the
  mirror's open-hour entries file that everyone fetches, §8.3). If it does
  not by the checkpoint after the named one, or on any later visit when the
  ballot is missing, the browser reposts the ballot to its next two relays
  and posts a `complaint {v, event, nostr_id, pi_hash, ack}` carrying the
  server's signed ack event, to the same two relays; auditors check
  complaints like any event (§12.6). A complaint is logged as `seen`. The
  page then shows the voter two links to pass on: the complaint on
  `njump.me` (any Nostr event, no account) and
  `/election/<slug>/complaint?id=<event>`, a static page that reads the
  complaint from the relays, checks the ack's signature by `nostr_pubkey`,
  the named checkpoint's signature by the log key and its entries on the
  mirror, and says in the reader's browser whether the ballot is there.
- With no ack within 20 s (60 s for a registration) the browser reposts to
  the next relay of its ranking.

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
  `stale_date`, `uid_used`, `id_used`, `closed`), each re-checkable by anyone
  from the stored body and the log;
- `seen {v, nostr_id, hash}` for a tagged event that is not a protocol body.

Every event id carrying the tag is thus listed, including copies of an
already logged body (`duplicate`), so an auditor never finds an unlisted
event. Entries ingested between two checkpoints are ordered by `pi_hash`, not
by arrival. Two ballots of one pseudonym within one checkpoint are both
logged, in that order: the `seq` rule (§6.3) compares with the ballots
logged at earlier checkpoints and with the pending ones, and auditors accept
any `seq` order within a checkpoint (the counted ballot is the highest
`seq`, never the last in log order).

A reason is the body's own: a failure of the server's (the database, the
verifier process) delays the judgement to the next tick rather than
signing a reject that a re-check would refute. The entries of a checkpoint,
their index rows and the pending rows' deletion are one transaction, so a
crash leaves either all of them logged or none; the tree takes its leaves
after the commit, and a server that starts again rebuilds it from the
index. One ingest at a time holds a lease on the log (a session advisory
lock of the database), one tallier likewise: a second one refuses to start
rather than acknowledge the same checkpoint index twice. A server reads a
relay from the newest `created_at` that relay delivered, minus two hours,
so a relay out of reach for a while is caught up from where it was left;
it reads the active relays of every setup logged, since browsers on an
older setup post to its relays until the newer one takes effect (§11).

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
of §5.1, the index shards of §6.3 (rebuilt hourly), and **hourly** entry
files `entries/<hour>` listing `(checkpoint, pi_hash, log_seq)` of every
entry of that hour, plus the open hour's file rewritten at each checkpoint.
A browser fetches whole hours, the same files as everyone, and finds its
receipt in them without naming its checkpoint. Every closed file is
immutable and under 20 MB; the repository rotates (the checkpoint names it)
by history size, before 150 MB. The full log, proofs included, stays on our server for auditors
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
hold a ballot whose decrypted values differ from the ballot the previous
tally counted for them** (a first ballot and a withdrawal count as changes;
a recast identical to the counted ballot does not, nor does a change undone
before the tally: the count is of net changes). There is no time cap: a cap would be the leak. The tallier
counts changes from its own per-ballot decryptions; the public cannot check
`K_REAL` exactly and does not need to, since it protects voters from the
public and the relays, and the tallier holds `sk` anyway. Auditors check what
is public: at least `K_REAL` distinct pseudonyms have a ballot in
`(prev_upto, upto]`, and `Δvoter ≤` the number of pseudonyms seen for the
first time in that range.

**Firing lag.** Firing at the very checkpoint that reaches `K_REAL` would
say that its last ballot was a real change. So once the count is reached,
the tallier fires at each following checkpoint with probability 1/2, one
coin per checkpoint however often it looks at it (geometric lag, a few
checkpoints on average), and `upto` is that checkpoint's last entry. The daily variant fires at a fixed `tally_at` time
of day, provided the count is reached, else it skips the day.

When the rule fires, the tallier:

1. takes, for every pseudonym, its latest valid ballot logged up to `upto`;
2. sums them per component: `S0 = Σ C0`, `S_j = Σ C_j`, and counts them, `c`;
3. knows each ballot's `p_j` from its own per-ballot decryption (a table
   over the valid rankings: 310 with seven candidates and the blank), so
   `P_j = Σ p_j` is known exactly and checked by
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
- At `T` our server logs what it holds (a checkpoint), then signs the
  **snapshot** `{v, event, at, root, count, checkpoint}`: `root` is the
  Merkle root (RFC 6962, the log's hashing) over the sorted ids of every
  protocol event it has seen. It appends it to the log as a `snapshot`
  entry, posts it to every relay (auditors reject a snapshot first seen after
  `T + 2 min`), puts the list on the mirror (`snapshot.json`) and checkpoints
  the entry. Auditors take their own snapshots: events they archived before
  `T − 5 min` must be in it; events in `[T − 5 min, T]` may or may not be.
- The tallier's final `tally` covers exactly the snapshot: its `upto` is the
  snapshot entry's seq (every ballot logged before it, judged when it was
  received). Then the **final shuffle** (§9.4), then
  `closed {v, event, at, final, snapshot_root}` (`final` the seq of the
  final tally, 0 without a ballot). Events seen after the snapshot are
  rejected with `closed`. After `closed` nothing is accepted and the final
  result is shown for good.
- 30 days after `closed`, the tallier destroys `sk` and its shares and our
  server appends `destroyed {v, event, at}`.

### 9.4 The final shuffle (optional, after launch)

With the final tally, our tallier may take the latest ballot of every
pseudonym, re-encrypt and permute them with a Bayer–Groth shuffle proof
(commitment keys derived by a verifiable hash-to-curve from the setup), and
decrypt each output with a DLEQ proof. The log then holds every final
ranking in clear, unlinkable to pseudonyms for the public. Anyone recomputes
Schulze, first choices or any other method from the ballots themselves; the
margins tally is the cross-check. The tallier knows the permutation, as it
knows `sk`. It protects nothing new and is the heaviest implementation item
(no Go library), so `closed.shuffle` is optional and verifiers accept its
absence; it does not gate the launch.

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

An auditor (`dm vote verify`, open source, incremental per checkpoint) checks,
from the log and the relays, at any time while the relays keep the events;
a continuous archive of the relays (`dm vote archive`, optional) adds the
times of first sighting:

1. **Log:** every checkpoint signed by the published key, each consistent
   with the previous (RFC 6962), its hash in Bitcoin; the checkpoints seen on
   at least two relays and on the mirror are one chain (no split view).
   Every time-based judgement below uses the `time` line of the signed
   note, never a record's `created_at`, which the server edits freely.
2. **Setup:** keys on the curve, distinct, not the identity nor `±G`;
   `pk_pok` valid; the `PK_j` compiled into the circuit equal to `setup.pk`;
   `vk_hash` equal to the key built from the published source with the pinned
   `nargo` and `bb`; `app_hash` equal to the reproducible build; later setups
   within §11.
3. **Registrations:** `|proof_date − checkpoint time| ≤ validity + 10 min`;
   every ZKPassport proof verifies locally with `proof_date` as the reference
   date (the stock verifier compares with today and would fail weeks later),
   with the certificate registry root valid at that date (Ethereum RPC): age,
   nationality, passport, not expired (the base proof does not check the
   expiry: the request carries `expiry_date >= <the proof's day>`, and the
   verifier requires that query, against that day), `custom_data =
   H(T_REG, event, ID)`, domain and scope; `uid` and `ID` unique.
4. **Tree:** every checkpoint root is the root of the leaves logged so far.
5. **Ballots:** every `ballot` verifies against its setup, a checkpoint root
   at most `anchor_age` older than its checkpoint, and the `seq` rule (a
   `seq` above every `seq` of the pseudonym logged at earlier checkpoints,
   `(nullifier, seq)` once); every `reject` deserves its reason, re-checked
   from the stored body and the log as it was at the reject's checkpoint
   (`bad_proof` by verifying the proof, `duplicate` and `stale_seq` by the
   ballots logged by then, `uid_used` and `id_used` by the registrations,
   `old_root`, `unknown_setup`, `stale_date` and `closed` by the
   checkpoint's time, `malformed` by parsing).
6. **Withholding (P1):** the auditor reads every tagged event of the relays
   (`--relays`: ballots, registrations, complaints, acks, checkpoints, the
   snapshot) and judges each protocol event by its acknowledgement:
   - acknowledged: the named checkpoint's entries list the event (`pi_hash`
     for a ballot, the body hash for a registration, the `nostr_id` for a
     reject or a `seen`) with the acknowledged outcome; a miss is
     **withholding, proven** by the signed ack;
   - not acknowledged, body logged (a repost whose first copy was taken):
     a relay-copy discrepancy, reported, not withholding;
   - not acknowledged, body not logged: **an unacknowledged event**, listed
     as a finding with its relay and `created_at`; with an archive, the
     time an auditor saw it: an event archived at `t` must be listed by the
     first checkpoint the auditor saw after `t + 10 min`, and one that is
     not is withholding attributed to our server (a relay serves what it
     holds to every subscriber alike), provided the event's `created_at`
     is within an hour of that sighting: the server reads a relay from the
     newest time it delivered minus two hours, so an event dated further
     back never reaches a live reader, and a browser corrects its clock
     from the checkpoints (§7.1).
   Checkpoints seen on the relays must equal the log's of the same index
   (no split view). Complaints (§7.2) are judged by the ack they carry,
   after its signature by `nostr_pubkey`: a complaint whose ack is not
   honoured by the log is withholding, proven; one whose ack is honoured is
   closed (the ballot was late on the mirror, not missing). A registration's
   ack names the posted body's hash while the log holds the canonical body,
   so a complaint about a registration is judged by the relay's copy of the
   event; when no relay holds it any more it is reported, not judged. The
   setup must name `nostr_pubkey`: an ack under any other key proves
   nothing.
7. **Tallies:** the latest valid ballot per pseudonym up to `upto`, their
   count `c`, their sums equal to `s0, s`, every digit within `[0, 2c]`, every
   DLEQ proof valid, the public checks of §9.1, `tally_min_gap` respected, no
   tally inside a blackout, and the results shown computed from the decrypted
   values.
8. **Close:** the snapshot on the relays (first seen by `T + 2 min` with an
   archive), taken at or after `closes`, logged once; every protocol event
   on the relays with `created_at` before `T − 5 min` in the snapshot's
   list (`snapshot.json`); `closed` naming its root and a final
   tally whose `upto` is the snapshot entry; no `ballot` or `register`
   logged after the snapshot; the shuffle proof and the output decryptions valid when
   present and the plaintext rankings consistent with the final tally,
   nothing accepted after `closed`, `destroyed` logged.

The voter's browser checks for itself: the ack of its ballot; its `pi_hash`
in the hourly entries files it fetches, the same files every visitor fetches;
checkpoints that only grow; the latest tally's DLEQ proofs against the sums
in the tally body (which prove the decryption of what the tally claims; the
full check of the sums is the auditor's, or the browser's on request: every
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
| `R_MIN` | 32 | later registrants a first ballot's root must share, when possible |
| `ANCHOR_AGE` | 24 h | how old a ballot's root may be |
| `validity` | 1 h | ZKPassport proof age, and the registration freshness bound |
| `CHECKPOINT_PERIOD` | 20 s | receipt latency |
| `tally_min_gap` | 0 (live) or 24 h (daily) | the owner's choice of rhythm |
| relays | ≥ 5 persistent, different operators, ≥ 32 KB messages | §7.1 |

Domain tags: `T_ID`, `T_NULL`, `T_NODE`, `T_REG`, `T_PRF`, `T_RELAY`,
`T_DLEQ`, `T_NONCE`, `T_POK`, `T_CT`. Nostr kinds in §7.1. Setup fields
added by this draft: `nostr_pubkey` (our server's), `tally_at` (daily
variant), `r_min`.

## 14. Before implementation

Formats to fix with the circuit, and test vectors to publish with it:

- the exact Poseidon2 call for every `H` (arity, the stdlib function), the
  tag → field rule, the `EmbeddedCurvePoint {x, y, is_infinite}` layout of
  public inputs, the `Field → EmbeddedCurveScalar` (lo/hi 128) conversion for
  `r_b` and `p_j`, the packing order, the tree index bit order, the Nostr
  event JSON of every kind, the ack, complaint and snapshot bodies, the
  canonical JSON rule, `pi_hash` and the snapshot root; the encodings of
  DLEQ `z` and `pk_pok` `(A, z)`; the ZKPassport result field used as
  `proof_date`; the tallier ↔ ingest interface (tally submission, its
  authentication, the log read); the `shuffle` entry if built;
- vectors: `ID` and nullifier for a given `(event, s)`; a depth-20 root with 3
  leaves; `p_j` for five rankings (blank only; `A, B, blank`; a withdrawal; a
  full 3-ranking; a reserved-slot reject); one complete ballot (public inputs,
  proof, `vk_hash`); a sum of 3 ballots with `c`, the digits and the results;
  one DLEQ with its nonce and challenge; one `pk_pok`; Schulze margins on a
  4-option matrix with a tie and a blank Condorcet win; a checkpoint, a
  consistency proof and an OTS stub; one negative vector per reject reason,
  registrations included; a register body, an ack and a complaint; a
  first-ballot root choice with 40 later leaves; a tally window holding an
  identical recast and a withdrawal.

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

## 15. Implementation status (2026-10-26)

Built, tested, run locally end to end (ten simulated voters through public
Nostr relays, a tally, the results on the page, the audit of the log):

- `circuits/ballot` (§6.2): 15,140 gates; 0.19 s native, 0.34 s in Chrome
  with 8 threads, 0.8 s single-threaded.
- `backend/vote`: Poseidon2, Grumpkin, the ballot arithmetic, DLEQ and
  key proofs, the tree; the setup (`dm vote keygen`, `dm vote setup`); the
  ingest over the `Mailbox` interface with its Nostr adapter (`dm vote
  ingest`) and its close snapshot; the tallier with `K_REAL` and the
  firing lag, the final tally over the snapshot and the `closed` entry
  (`dm vote tally`); the data mirror on a directory; the auditor
  (`dm vote verify`, §12.1–12.5, 12.7, 12.8; `--relays` reads the relays
  for §12.6: acks, unacknowledged events, complaints, the checkpoints'
  split view, the snapshot; `--archive` takes the continuous archive of
  `dm vote archive` with its first sightings).
- `frontend/src/lib/vote` and the vote page: the key (PRF passkey or 12
  words), the passport scan bound to the registration, the relays, the
  private path update, the prover warm-up with the service worker, the
  ballot, acknowledgements, the receipt, the results and counters; the
  complaint when the checkpoint after the acknowledged one does not list
  the ballot (reposted to two relays with the signed ack).

Differences from the text above:

- The registration acknowledgement carries no position (§4 step 4, as
  amended); the mirror also serves `event.json`, `candidates.json`,
  `circuit/ballot.json`, `log-key.txt` and `stats.json` (ballots logged,
  since the last tally, registrations).
- The prover asks bb.js for a 2^17-point SRS (4 MB, the smallest block it
  takes) plus its fixed 4 MB Grumpkin SRS: about 8 MB on a first visit,
  not 4–8.
- A local trial mode (`--dev-accept-any-passport`,
  `VITE_VOTE_SIMULATE_PASSPORT=1`) stands in for the ZKPassport app; the
  command refuses it on the host.

- The key keeps the last ballot's check (received, verified, missing);
  a later visit shows it and finishes it. The checkpoint subscription
  moves to the next relay of the ranking when a relay cannot be reached.

- The git-backed mirror (`dm vote ingest --mirror-git REMOTE`): each
  checkpoint one commit, its hash the token the note names, read through
  jsDelivr by commit and the latest files through the repository's pages
  (the browser's `Sources` tries a commit-aware mirror first for a token).
  The organisation is `tartuffeme` (Q3): the mirror is
  `github.com/tartuffeme/vote-data` (pages at
  `tartuffeme.github.io/vote-data`), the app `tartuffeme.github.io` (built
  by the code repository's workflow `vote-app.yml` from `npm run
  build:vote`, the hash of the build in the commit message: `app_hash`),
  the code `tartuffeme/mirror` (private until the licence is chosen).
- The ranking can be reordered with the keyboard (buttons on each slot)
  besides the drag.

- The device link (§4.2): the new device's QR code or URL, the sending
  device's confirmation, the sealed secret through a relay.

- Deployment (2026-10-27): the repositories exist under `tartuffeme`
  (the app and the data public, with the write deploy keys of the
  workflow and of the ingest); the ingest is the host service
  `ingest` built from `Dockerfile.ingest` (Debian: bb, git, ssh, Node,
  the election files of `deploy/ingest/<slug>/`); the API allows the
  app's origin and pins the log key's verifier (the workflow's variable
  `VOTE_LOG_VERIFIER_KEY`, the frontend's `VITE_VOTE_LOG_KEY`).

- Review (2026-10-27, three independent passes: spec against backend,
  the voter's journey, adversarial): the acknowledgement is written as
  pending before it is posted and logged at its checkpoint after a
  restart; the checkpoint is one transaction; one ingest and one tallier
  at a time (a lease); a server failure puts a message off instead of
  rejecting it; lower-case hex only (one pseudonym, one string); `seq`
  stored as 64-bit; relay cursors from delivered times, every setup's
  relays read; the tallier's coin once per checkpoint and net changes;
  the auditor judges by signed times, accepts any `seq` order within a
  checkpoint, checks `K_REAL`, `tally_min_gap` and the count, re-checks
  every reject's reason, requires `nostr_pubkey`, attributes an
  unacknowledged event only when created within an hour of its sighting;
  the passport verifier pins the non-salted identifier and the validity.
  The browser's findings (registration failures, key persistence, passkey
  recovery, polling, the by-commit fallback, ack signatures, the device
  link's confirmation code, the clock, DLEQ checks) are in the same
  change set.

Not built yet: the final shuffle (§9.4). Not done yet: the election's
own setup in production (the tallier's keygen, `dm vote setup` with the
relays, its files committed under `deploy/ingest/presidentielle-2027/`,
`app_hash` from the first published build), the tallier's machine.
