# tartuffe.me, the site

SvelteKit 5 (Svelte 5 runes), TypeScript, Vite. The API it reads is the Go
server of the repository (`dm web serve`); the vote page talks to no server
of ours at all: it reads the public data mirror and posts through Nostr
relays (`src/lib/vote`, `docs/vote/SPEC.md`).

```bash
npm install
npm run dev -- --port 3000     # expects the API on 8080 (VITE_API_BASE)
npm run check                  # svelte-check
npx vitest run                 # unit tests (src/lib/**/*.test.ts)
npm run build                  # production build
```

Settings are `VITE_*` variables (`src/lib/config.ts`; the vote's in
`src/lib/vote/sources.ts`): `VITE_API_BASE`, `VITE_VOTE_MIRROR` (a directory
URL of the mirror), `VITE_VOTE_MIRROR_REPO` (owner/repo, read through
jsDelivr by commit), `VITE_VOTE_LOG_KEY` (the log's verifier key, pinned),
`VITE_ZKPASSPORT_DOMAIN`.

Translations are in `src/lib/i18n.ts` (`tr(key, lang)`); French copy keeps
its typography (a non-breaking space before `: ; ? !`).
