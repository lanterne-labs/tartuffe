# LiteLLM Proxy + Tailscale Sidecar (the host)

Routes LLM and embedding traffic from the `dm` the host service to a home AI box
via Tailscale, with automatic fallback to a cloud provider (e.g. OpenRouter)
when the home box is unreachable.

## Architecture

```
dm (Go backend)  ──>  litellm.host.internal:4000
                              │
                              │  primary    fallback
                              ▼              ▼
                       gemma-box:11434   openrouter.ai
                       (via tailnet)     (direct)
```

LiteLLM is the only thing on the host joined to your tailnet — `dm` is unchanged
and just talks plain HTTP to LiteLLM via the host's private network.

## The host setup

1. **Create new service** in your host project.
   - Source: this repo, root directory `deploy/litellm`.
   - Builder: Dockerfile (auto-detected).
   - Port: `4000`.
   - **Do NOT generate a public domain** — service must stay project-internal.
2. **Set env vars** (see below).
3. **Repoint `dm` service env vars**:
   - `OPEN_AI_API=http://litellm.host.internal:4000/v1`
   - `OPEN_AI_KEY=anything` (no auth required for internal calls)
   - `OPEN_AI_CHAT_MODEL=chat`
   - `OPEN_AI_EMBED_MODEL=embedding`

## Required env vars on the `litellm` service

| Var | Purpose | Example |
|---|---|---|
| `TS_AUTHKEY` | Tailscale auth key (ephemeral, reusable, tagged) | `tskey-auth-...` |
| `TS_HOSTNAME` | Optional, defaults to `host-litellm` | `host-litellm` |
| `OPEN_AI_API` | Primary base URL on tailnet | `http://gemma-box:11434/v1` |
| `OPEN_AI_KEY` | Primary key (Ollama: any non-empty) | `ollama` |
| `OPEN_AI_CHAT_MODEL` | Primary chat model | `gemma3:27b` |
| `OPEN_AI_EMBED_MODEL` | Embedding model (used on BOTH primary and fallback — must resolve to same weights on both) | `qwen/qwen3-embedding-8b` |
| `FALLBACK_URL` | Fallback base URL | `https://openrouter.ai/api/v1` |
| `FALLBACK_KEY` | Fallback API key | `sk-or-...` |
| `FALLBACK_CHAT_MODEL` | Fallback chat model (may differ from primary) | `qwen/qwen3-72b-instruct` |

## Tailscale setup

1. Tailscale admin → Settings → Keys → **Generate auth key**:
   - Reusable: yes
   - Ephemeral: yes
   - Tags: `tag:host` (define ACL for it first if not already)
2. Paste the key as `TS_AUTHKEY` on the `litellm` the host service.
3. On the home box: ensure Tailscale is installed, logged in, and the AI server
   (Ollama / llama-swap) is bound to `0.0.0.0:11434` so it's reachable from the tailnet.
4. **Align the embedding model name** between providers so the same string
   works on both:

   ```
   ollama cp <existing-tag> qwen/qwen3-embedding-8b
   ```

   Verify:
   ```
   curl http://gemma-box:11434/v1/embeddings \
     -d '{"model":"qwen/qwen3-embedding-8b","input":"test"}' \
     -H 'Content-Type: application/json'
   ```

## Verification (post-deploy)

From the `dm` the host shell:

```sh
# primary should hit home box
curl http://litellm.host.internal:4000/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"chat","messages":[{"role":"user","content":"ping"}]}'

# embeddings
curl http://litellm.host.internal:4000/v1/embeddings \
  -H 'Content-Type: application/json' \
  -d '{"model":"embedding","input":"hello"}'
```

To test fallback: stop the LLM server on the home box, repeat — should return
output from the fallback provider after a brief retry.

## Local development

LiteLLM is **not** used locally. `dm` reads `OPEN_AI_API` from `.env` and hits
the cloud provider directly, exactly as before. No Tailscale, no LiteLLM, no
new env vars in your local `.env`.
