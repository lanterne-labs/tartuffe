# Claude Bot Rules

These rules are authoritative for any Claude run that maintains this
repository (cron jobs, `@claude` mentions, automated reviews).

Always also read the repository's `CLAUDE.md` files (root and any nested
ones such as `backend/CLAUDE.md`, `frontend/CLAUDE.md`) and apply them in
addition to these rules. If a project rule contradicts a rule here, the
project rule wins.

## Work

- Use subagents for distinct phases when it makes sense (e.g. one subagent
  implements, another reviews the diff before pushing).
- Read `CHANGES.md` at the project root if it exists, before making
  changes — it captures past mistakes you must not repeat.
- For changes that affect API or externally observable behavior, append a
  small section to `CHANGES.md` describing what changed and why. Do not
  log every bug fix.
- For small experiments or scripts, prefer `bash` or `python` even if the
  main codebase is in another language, unless the script must live in
  the project's language to be useful.

## Performance

- Minimize round trips between application code and the database. If a
  single SQL query can do filtering / aggregation that the backend would
  otherwise do in a loop, write the SQL query.

## Bugs

- Never silently bypass an issue. If you can fix the root cause, do that.
  If you can't, return an error or panic — never `if broken { return }`
  with no signal. Fail loudly and early.
- If a test fails for a reason unrelated to the change you're making, do
  NOT claim success. Report the failing test, its likely cause, and ask
  (in the PR description or an issue comment) before "fixing" it. The
  human picks the direction.

## Code

- Remove unused functions you orphaned by your change. No dangling code.
- Every new feature ships with a test. If a near-by test exists and is
  cheap to update, update it. Otherwise add a new test.
- Keep modules well-encapsulated. Callers should not need to know
  internal details of a module you're touching. Get the API boundary
  right.
- Never introduce a new environment variable without an explicit ask in
  the issue or PR description. If you think one is needed, propose it in
  a comment first; don't add it.

## Collaboration

- On merge conflicts: prefer the logic from the branch that contains the
  newer / corrective change (usually the PR branch). For pure variable
  renames or trivial conflicts, decide on your own. For real logic
  conflicts where both sides made meaningful changes, leave a PR comment
  flagging the choice you made and why, so a human can override.

## Voice

- Technical, direct, terse. No filler ("I'll take a look"), no apologies,
  no "great point". State conclusions and reasoning.
- Push back when warranted. If a request is wrong, would introduce a bug,
  or contradicts the codebase's rules, refuse and propose an alternative.
  Do not silently comply with broken specs.
