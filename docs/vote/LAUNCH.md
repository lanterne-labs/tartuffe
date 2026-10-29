# Launching the vote: the runbook

The order of operations to put tartuffe-vote/2 (SPEC.md) in production for
an event, with the exact commands. Three machines: the **tallier** (a box
with no inbound access, the only holder of the election's secret keys),
the **ingest** (the host service `ingest`), and a **dev machine** with
the repository, `nargo` and `bb` (the setup compiles the circuit).

## 0. Before anything

- PR #48 merged on `main` (the host services `mirror`, `worker`,
  `frontend` redeploy main; the migrations of the branch apply at the
  API's start).
- The secrets on the host (the owner's `set-secrets.sh`, or by hand):
  `VOTE_LOG_KEY` and `AUTH_SUBJECT_KEY` on `mirror` and `ingest`;
  `VOTE_NOSTR_KEY` and `VOTE_MIRROR_SSH_KEY` on `ingest`. `VOTE_LOG_KEY`
  comes from `dm vote log-key`: set once, never changed (every browser that
  saw a checkpoint would report a fault). Its verifier key is pinned as
  `VITE_VOTE_LOG_KEY` on the `frontend` service and as the Actions variable
  `VOTE_LOG_VERIFIER_KEY` of the code repository.
- The event exists (`poll_events`: `presidentielle-2027`, its candidates,
  its close and blackouts): `dm` on the production database, or the
  seed migration.

## 1. The keys (tallier)

```sh
dm vote keygen presidentielle-2027 --out /secure/keys.json
```

Writes `keys.json` (the election's decryption keys and the tally signing
seed: it never leaves the tallier; back it up offline) and `keys.pub.json`
(the public keys, their proofs of knowledge and `tally_pubkey`): copy the
`.pub.json` to the dev machine.

The ingest's relay key, once, on the dev machine:

```sh
dm vote nostr-key              # prints VOTE_NOSTR_KEY and its public key
```

`VOTE_NOSTR_KEY` goes to the `ingest` service; the public key to the setup
below (`--nostr-pubkey`). With `VOTE_NOSTR_KEY` set, `dm vote nostr-key`
prints the public key again.

## 2. The setup (dev machine, against the production database)

```sh
DATABASE_URL=<production> dm vote setup presidentielle-2027 \
  --keys keys.pub.json \
  --relays wss://nos.lol,wss://relay.primal.net,wss://nostr.mom \
  --nostr-pubkey <public key of VOTE_NOSTR_KEY> \
  --dry-run                      # prints the setup entry; drop the flag to log it
```

Compiles `circuits/ballot` for the public keys (minutes), writes the
artifact, the verification key and the setup body to
`circuits/ballot/target/setup-presidentielle-2027/`, checks the entry
(`tally_pubkey`, `nostr_pubkey`, the relays, the options from the event)
and appends it to the log. The relays: the ones `dm vote relays probe`
found to keep both kinds (docs/vote/relays-probe.json); at least three.
Optional flags: `--k-real 1` (the launch value: results at every vote;
the default 10 batches ten changed ballots per update), `--tally-min-gap`,
`--app-hash` (step 4, or a later setup), `--srs-hash`.

Commit the setup files for the ingest's image:

```sh
mkdir -p deploy/ingest/presidentielle-2027
cp circuits/ballot/target/setup-presidentielle-2027/{vk,ballot.json} deploy/ingest/presidentielle-2027/
git add deploy/ingest && git commit -m "vote: the setup files of presidentielle-2027" && git push
```

## 3. The ingest (the host)

The service `ingest` builds `Dockerfile.ingest` with the start command

```
dm vote ingest presidentielle-2027 --vk /app/vote/presidentielle-2027/vk \
  --artifact /app/vote/presidentielle-2027/ballot.json \
  --mirror-dir /app/mirror --mirror-git git@github.com:tartuffeme/vote-data.git
```

and the variables `DATABASE_URL`, `VOTE_LOG_KEY`, `VOTE_NOSTR_KEY`,
`VOTE_MIRROR_SSH_KEY` (the private half of the write deploy key of
`tartuffeme/vote-data`), `AUTH_PROVIDERS=zkpassport`, `AUTH_SUBJECT_KEY`,
`ZKPASSPORT_DOMAIN=tartuffeme.github.io`. Connect it to `tartuffeme/mirror`
(branch `main`) once the setup files are on main. One instance only: a
second one refuses to start (the lease).

At its start the ingest publishes the setup files on the mirror, then reads
the relays, checkpoints every 20 s when something happened, stamps the
checkpoints and upgrades the stamps hourly.

## 4. The app (GitHub Pages)

The workflow `.github/workflows/vote-app.yml` builds the static vote app on
every push to `main` touching `frontend/**` and pushes it to
`tartuffeme/tartuffeme.github.io` with the build's hash in the commit
message. That hash is the `app_hash`: log it with a later setup
(`dm vote setup … --version 2 --app-hash <hash>`) once the build is the
one voters run, and after every change of the app.

## 5. The tallier (its machine)

```sh
DATABASE_URL=<production, read and append> dm vote tally presidentielle-2027 --keys /secure/keys.json
```

Decrypts every logged ballot for itself and publishes a signed tally once
K_REAL pseudonyms changed their ballot; at the close, the final tally and
the signed `closed` entry. One instance only (the lease).

## 6. Checks

- `dm vote verify presidentielle-2027 --api https://tartuffe.me --vk circuits/ballot/target/setup-presidentielle-2027/vk --relays setup --mirror-cdn tartuffeme/vote-data`
  from any machine: the log, the proofs, the relays, the stamps against
  Bitcoin, the mirror at each commit. Exit 0 and no `PROBLEM` line.
- One real phone: the ZKPassport scan, a ballot, the receipt
  (`mirror-f04`), and a passkey restore.
- Before opening to the public: the test ballots purged and the `noop`
  provider dropped (`mirror-gfv`); the legal pages (`mirror-a94.10`).

## What never changes after the setup

`n_cap`, `blank`, the keys, `tally_pubkey`, `vk_hash`, `srs_hash`,
`k_real`, `anchor_age`, `closes`, `blackouts` (SPEC §11). A change to any
of them is a new event.
