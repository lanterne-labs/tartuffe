# The ingest's election files

One directory per election slug, with the two outputs of `dm vote setup
<slug>` that the ingest reads (`Dockerfile.ingest` copies this directory to
`/app/vote/`):

- `vk`: the circuit's verification key (its hash is the setup entry's
  `vk_hash`: the ingest refuses a ballot proved for another circuit);
- `ballot.json`: the compiled circuit, published on the mirror for the
  browsers.

Both come from `circuits/ballot/target/setup-<slug>/` after the setup was
logged. The secret keys of the election (`keys.json` of `dm vote keygen`)
never come here: they stay on the tallier.
