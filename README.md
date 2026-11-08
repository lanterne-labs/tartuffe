# Tartuffe

**What French politicians said, what they did, and where the two disagree.**

[tartuffe.me](https://tartuffe.me) reads what politicians say and do: their speeches, interviews, tweets, the press, and their votes in the Assemblée nationale. It turns each statement and each act into a dated, sourced fact. It then looks for the places where a person's words contradict their own acts, their promises, or their earlier positions, and shows every contradiction with its sources, so anyone can check it.

From those contradictions each person gets a **Tartuffe-Score**, a barometer from « cohérent » to « tartuffe ». The formula, its inputs and every correction are public on the site.

## What it does

- **Collects** statements and acts from the news, X, vie-publique.fr and the open data of the Assemblée nationale.
- **Extracts facts** with an LLM: one claim or one act, its date, the person's own words, and the source.
- **Groups facts into stances**, one per subject, so the record of a person reads as positions over time.
- **Detects contradictions** between facts close in meaning, judged by an LLM against [written rules](backend/promptrules). A change of opinion over time is an evolution, not a contradiction: anyone may change their mind.
- **Scores** each person from their contradictions and their number of facts ([`backend/core/honesty.go`](backend/core/honesty.go)).
- **Lets readers push back.** Signed-in users can report a fact or a contradiction. A person reviews each report, and accepted ones go to a [public corrections log](https://tartuffe.me/corrections).
- **Runs an anonymous pre-poll** for the 2027 presidential election. Eligibility is proved with a passport through zero-knowledge proofs, and the ballots are counted by [isoloir](https://github.com/lanterne-labs/isoloir), a verifiable voting system in its own repository.

## How it is built

| Part | Stack |
|---|---|
| API and worker | Go, PostgreSQL with pgvector, Chi |
| Site | SvelteKit 5 |
| LLMs | Claude, or a local model behind an OpenAI-compatible API |
| Hosting | the host, with Prometheus and Grafana |

## Documentation

- [How the site works](https://tartuffe.me/about): the method, the sources and the score, for readers.
- [CLAUDE.md](CLAUDE.md): the full guide for developers: commands, architecture, conventions and deployment.
- [DEVELOPMENT.md](DEVELOPMENT.md): the local workflow.
- [CHANGES.md](CHANGES.md): the history of the changes.
- [Privacy](https://tartuffe.me/about/privacy) and [how the vote works](https://tartuffe.me/election/presidentielle-2027/fonctionnement).

## Quick start

```bash
make db-start                    # local Supabase
cp .env.example .env             # set DATABASE_URL and an LLM key
go build -o dm . && ./dm web serve --port 8080
cd frontend && npm install && npm run dev -- --port 3000
```

`make test` runs the Go tests.

## License

[GNU AGPL v3](LICENSE). You may run, study, change and share this code. If you run a modified version as a public service, you must publish your changes under the same license.

Made by [Lanterne Labs](https://github.com/lanterne-labs).
