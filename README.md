# CMDP Doc Bot

A Discord bot that answers questions about a Minecraft plugin using the **OpenCode CLI** over locally cloned documentation and source code repositories.

The bot maintains its own working copies of the target Git repositories and periodically polls for upstream changes so its answers always reflect the latest documentation. When a user `@mentions` the bot in a Discord channel, it prompts a long-lived `opencode serve` subprocess (configured with a strict system prompt) to search the cloned repositories, read relevant files, and reply with a grounded, scope-locked answer. Each Discord user gets a persistent opencode session (30-minute idle TTL) so the agent can reference prior context within a conversation.

---

## Table of Contents

- [Overview](#overview)
- [How It Works](#how-it-works)
- [Prerequisites](#prerequisites)
- [Configuration (`.env`)](#configuration-env)
- [Running with Docker Compose](#running-with-docker-compose)
- [Running Locally (without Docker)](#running-locally-without-docker)
- [Running the Test Suite](#running-the-test-suite)
- [Project Layout](#project-layout)
- [Notes](#notes)

---

## Overview

CMDP Doc Bot is built for a single purpose: provide accurate, source-grounded support for a specific Minecraft plugin inside Discord. To keep answers trustworthy it enforces scope restriction:

- **Strict system prompt** — the LLM is instructed to act *only* as a plugin support agent and to use *only* the retrieved context. Anything outside that scope yields a polite refusal rather than a hallucinated answer.
- **OpenCode Agent** — the bot uses the `opencode` CLI to navigate the codebase, read files, and search for information, ensuring answers are based on the actual repository contents.

Additional features:

- **Repository syncing** — clones or `git pull`s each configured repository on a configurable interval.
- **Preprocessing** — automatically generates an `AGENT.md` overview file in each cloned repository to help the answering agent navigate the codebase efficiently.
- **Per-user rate limiting** — an in-memory sliding window (default 5 questions / 10 minutes per user) prevents abuse and controls LLM cost.
- **Async, non-blocking** — LLM calls are asynchronous, and a typing indicator is shown while the answer is being produced.

## How It Works

1. **Startup** — The bot loads settings from `.env`, dynamically generates its own `opencode.json` configuration, copies the `docbot` agent persona into `~/.config/opencode/agents/`, starts a long-lived `opencode serve` subprocess (HTTP API on `127.0.0.1:4096`), and wires together the repository manager, rate limiter, per-user session manager, LLM client, and Discord client.
2. **Polling & Preprocessing** — A background task (`discord.ext.tasks.loop`) runs every `POLL_INTERVAL_MINUTES` minutes, pulls each repository into `data/repos/<name>`, and spawns an `opencode` subprocess to generate an `AGENT.md` file in the root of any repository that was updated.
3. **Answering** — On an `@mention`, the bot strips its mention from the message, checks the user's rate limit, looks up (or creates) the user's opencode session, acquires a per-user lock, and prompts the session via the opencode HTTP API (`POST /session/:id/message`). It concatenates the `text` events from the streaming NDJSON response and returns the final answer. On a non-empty answer the session's `last_active` is refreshed so the TTL clock keeps rolling.
4. **Session sweeping** — A background task runs every minute, deletes opencode sessions that have been idle past `SESSION_TTL_MINUTES`, and removes them from the in-memory mapping. On the next message from that user a fresh session is created.

### Sessions

Each Discord user gets **one opencode session** that persists across messages for `SESSION_TTL_MINUTES` minutes of idle time (default 30). This lets the docbot agent reference prior context within a conversation — e.g. "follow-up: what about the second argument?" — without the bot re-sending the full history each time.

The session mapping is **in-memory**: on bot restart every user starts a fresh session. The sessions themselves persist on the opencode server's storage (`~/.local/share/opencode/storage/`), but the bot does not re-attach to them after a restart — this avoids reusing a session created under a different model/provider/variant. A background sweeper deletes expired sessions on the server so they don't accumulate.

Sessions are tagged with metadata `{"discordUserId": "<id>"}` and titled `discord:<id>` so they can be queried via `opencode` tooling if needed. The per-user `asyncio.Lock` ensures the same user's prompts serialize (different users run in parallel), which avoids the cross-subprocess race that `opencode run --session` would hit (issue #11699).

## Prerequisites

- **Docker** and **Docker Compose** (recommended) — any recent version with the Compose v2 plugin (`docker compose`).
  - Verify with: `docker --version && docker compose version`
- **Python 3.11+** — only required if you want to run the bot or the test suite locally without Docker.
- **OpenCode CLI** — required to be installed and available on the system `PATH` if running locally. The bot uses the `opencode serve` subcommand (any modern build that includes the HTTP server; the binary already on the host is fine).
- A **Discord bot token** with the **Message Content** privileged intent enabled (see [Discord Developer Portal](https://discord.com/developers/applications)).
- An **LLM provider + API key**. The bot targets OpenCode's built-in providers out of the box (loaded from [Models.dev](https://models.dev/) — e.g. OpenCode Zen, Anthropic, OpenAI, Google, Groq, ...); set `LLM_API_KEY` and the bot maps it to the provider's declared env var at startup. A `custom-llm` escape hatch (`LLM_BASE_URL` + `LLM_API_KEY`) remains for arbitrary OpenAI-compatible endpoints. See [Provider Configuration](#provider-configuration) below.

## Configuration (`.env`)

All runtime configuration is supplied through environment variables, loaded from a `.env` file by `pydantic-settings`. Copy the provided template and fill in your values:

```bash
cp .env.example .env
```

`.env` fields:

| Variable                | Required | Default                   | Description                                                                                                              |
| ----------------------- | -------- | ------------------------- | ------------------------------------------------------------------------------------------------------------------------ |
| `DISCORD_TOKEN`         | yes      | —                         | Authentication token for the Discord bot account.                                                                        |
| `LLM_PROVIDER`          | no       | `opencode`                | Provider ID known to OpenCode / [Models.dev](https://models.dev/) (e.g. `opencode`, `anthropic`, `openai`, `google`, `groq`). Set to `custom-llm` for the legacy OpenAI-compatible shim (requires `LLM_BASE_URL`). See [Provider Configuration](#provider-configuration). |
| `LLM_API_KEY`           | yes      | —                         | API key for the LLM provider. The bot maps this to the provider's declared env var at startup (e.g. `OPENCODE_API_KEY` for `opencode`, `ANTHROPIC_API_KEY` for `anthropic`). |
| `LLM_MODEL`             | no       | `deepseek-v4-flash-free`  | Model name (without the provider prefix) to use for LLM chat generation. Use `opencode models` to list what's available for the auth'd provider. |
| `LLM_VARIANT`           | no       | —                         | Optional reasoning-effort variant (model-specific), e.g. `max`, `high`, `low`. Leave unset to use the model's default. List valid variants with `opencode models --verbose`. |
| `LLM_BASE_URL`          | only for `custom-llm` | —             | Base URL of a custom OpenAI-compatible LLM endpoint. Only required when `LLM_PROVIDER=custom-llm`; ignored for built-in providers. |
| `GITHUB_TOKEN`          | no       | —                         | GitHub token used to authenticate when cloning/pulling private repositories. Automatically injected into `https://github.com/...` URLs. |
| `REPO_URLS`             | yes      | —                         | Git repositories to clone and search. Accepts a comma-separated list **or** a JSON array (see examples below).           |
| `POLL_INTERVAL_MINUTES` | no       | `10`                      | Minutes between repository polling cycles.                                                                               |
| `OPENCODE_SERVER_HOST`  | no       | `127.0.0.1`               | Hostname the long-lived `opencode serve` subprocess binds to. Loopback only by default (the bot and the server run on the same host). |
| `OPENCODE_SERVER_PORT`  | no       | `4096`                    | TCP port the `opencode serve` subprocess binds to.                                                                       |
| `OPENCODE_SERVER_PASSWORD` | no    | auto-generated            | HTTP Basic Auth password for the `opencode serve` subprocess. When unset, a secure random password is generated at startup. Set explicitly to keep the same password across restarts (useful for `opencode web` debugging). |
| `SESSION_TTL_MINUTES`   | no       | `30`                      | Per-user opencode session idle TTL in minutes. After this idle period, the user's session is deleted and a fresh one is created on the next message. |

`REPO_URLS` examples — both forms are valid:

```dotenv
# Comma-separated
REPO_URLS=https://github.com/example/plugin-docs.git,https://github.com/example/plugin-source.git

# JSON array
REPO_URLS=["https://github.com/example/plugin-docs.git","https://github.com/example/plugin-source.git"]
```

> **Tip:** The `.env` file is read by both the local run and Docker Compose (via the `env_file` directive). It is listed in `.dockerignore` so secrets are never baked into the image.

### Provider Configuration

The bot does **not** call an LLM endpoint directly — it starts a long-lived `opencode serve` subprocess at startup and talks to its HTTP API to create/continue per-user sessions. OpenCode resolves the provider from its built-in catalog (loaded at runtime from [Models.dev](https://models.dev/api.json)). This means you get model presets, capabilities, cost metadata, and reasoning-effort variants for free, without hand-maintaining a custom provider block, and each Discord user gets a persistent, stateful conversation with the docbot agent.

**Default — OpenCode Zen.** With `LLM_PROVIDER=opencode` (the default) the bot uses [OpenCode Zen](https://opencode.ai/docs/zen/); set `LLM_API_KEY` to your Zen key and `LLM_MODEL` to a Zen-hosted model (e.g. `deepseek-v4-flash-free`). At startup the bot publishes `LLM_API_KEY` into the `OPENCODE_API_KEY` env var so OpenCode's built-in `opencode` provider picks it up.

**Switching to another built-in provider.** Set `LLM_PROVIDER` to the provider ID, `LLM_API_KEY` to that provider's key, and `LLM_MODEL` to a bare model name (no provider prefix). The bot maps `LLM_API_KEY` to the right env var automatically:

| `LLM_PROVIDER`  | Env var written at startup          |
| --------------- | ----------------------------------- |
| `opencode`      | `OPENCODE_API_KEY`                  |
| `anthropic`     | `ANTHROPIC_API_KEY`                 |
| `openai`        | `OPENAI_API_KEY`                    |
| `google`        | `GOOGLE_GENERATIVE_AI_API_KEY`      |
| `groq`          | `GROQ_API_KEY`                      |
| `mistral`       | `MISTRAL_API_KEY`                   |
| `deepseek`      | `DEEPSEEK_API_KEY`                  |
| `openrouter`    | `OPENROUTER_API_KEY`                |
| `xai`           | `XAI_API_KEY`                       |
| `cohere`        | `COHERE_API_KEY`                    |
| `vercel`        | `AI_GATEWAY_API_KEY`                |
| `azure`         | `AZURE_API_KEY` (+ `AZURE_RESOURCE_NAME`) |
| `perplexity`    | `PERPLEXITY_API_KEY`                |
| `deepinfra`     | `DEEPINFRA_API_KEY`                 |
| `togetherai`    | `TOGETHER_API_KEY`                  |
| `cerebras`      | `CEREBRAS_API_KEY`                  |

For example, to use Anthropic directly:

```dotenv
LLM_PROVIDER=anthropic
LLM_API_KEY=sk-ant-...
LLM_MODEL=claude-sonnet-4-5
# Optional reasoning-effort variant:
LLM_VARIANT=max
```

An explicit env var already set in the environment wins over `LLM_API_KEY` (the bot uses `os.environ.setdefault`), so users who pre-set `ANTHROPIC_API_KEY` are not clobbered.

**Providers that don't use API keys.** For `github-copilot` (OAuth), `google-vertex` (ADC), or `amazon-bedrock` (AWS creds), the bot logs a warning and skips the automatic env-var mapping. Run `opencode auth login` once on the host before starting the bot, or set the appropriate env vars yourself.

**The `custom-llm` escape hatch.** If you need an OpenAI-compatible endpoint that isn't a built-in OpenCode provider, set `LLM_PROVIDER=custom-llm` together with `LLM_BASE_URL` and `LLM_API_KEY`:

```dotenv
LLM_PROVIDER=custom-llm
LLM_BASE_URL=https://api.example.com/v1
LLM_API_KEY=sk-...
LLM_MODEL=my-model
```

In this mode the bot writes a custom OpenAI-compatible `provider` block into `opencode.json` (the legacy behaviour). `LLM_BASE_URL` is required for this mode; if it is omitted the provider block is skipped and a warning is logged. **Note:** the default `LLM_MODEL` is `deepseek-v4-flash-free` (a model that only exists on the `opencode` Zen provider); `custom-llm` users should set `LLM_MODEL` explicitly to a name their endpoint recognises.

**Using `opencode auth login` instead.** If you'd rather use `opencode auth login`, run it once on the host before starting the bot; OpenCode reads `~/.local/share/opencode/auth.json` automatically. (`opencode auth login` is interactive — it prompts for the API key via a TTY password prompt — so it is not suitable for headless containers; the env-var flow above is the recommended path for Docker.)

### Discord intents

The bot requires the **Message Content** privileged intent to read `@mention` questions. Enable it on your application's **Bot** page in the Discord Developer Portal before running.

## Running with Docker Compose

This is the recommended way to run the bot. Docker Compose builds the image from the local `Dockerfile`, mounts `./data` for persistent state, and injects configuration from `.env`.

1. **Configure the environment** (see [Configuration](#configuration-env)):

   ```bash
   cp .env.example .env
   # edit .env and fill in DISCORD_TOKEN, LLM_API_KEY, REPO_URLS
   ```

2. **Build and start the bot**:

   ```bash
   docker compose up --build -d
   ```

3. **Follow the logs**:

   ```bash
   docker compose logs -f bot
   ```

4. **Stop the bot**:

   ```bash
   docker compose down
   ```

The `./data` directory is bind-mounted to `/app/data` inside the container, so the cloned repositories (`data/repos/`) persist across restarts. The container restarts automatically (`restart: unless-stopped`).

To rebuild after changing code or dependencies:

```bash
docker compose up --build -d
```

## Running Locally (without Docker)

1. **Create and activate a virtual environment** (Python 3.11+):

   ```bash
   python -m venv .venv
   source .venv/bin/activate   # Windows: .venv\Scripts\activate
   ```

2. **Install dependencies** (includes test dependencies):

   ```bash
   pip install -r requirements.txt
   ```

3. **Configure the environment**:

   ```bash
   cp .env.example .env
   # edit .env and fill in the required values
   ```

   Ensure `git` and `opencode` are installed and available on your `PATH`.

4. **Run the bot**:

   ```bash
   python main.py
   ```

The bot reads `.env` from the current working directory and writes persistent state to `./data`.

## Running the Test Suite

The test suite uses `pytest` with `pytest-asyncio`. Test dependencies are already included in `requirements.txt`.

**From a local environment:**

```bash
source .venv/bin/activate   # if not already active
pip install -r requirements.txt
pytest
```

**Useful options:**

```bash
pytest -v              # verbose, one line per test
pytest tests/test_client.py   # run a single module
pytest -k rate_limiter # run tests matching a keyword
```

All tests are hermetic — they use injected fakes for the LLM client, Git manager, and Discord client, so no live API keys or network access are required.

## Project Layout

```
cmdp_doc_bot/
├── agent/              # OpenCode agent persona
│   └── docbot.md       #   System prompt and instructions for the answering agent
├── bot/                # Discord client and background polling task
│   ├── client.py       #   DocBot: mention handling, rate limiting, typing indicator
│   └── tasks.py        #   Repository polling task (clone/pull + AGENT.md generation)
├── core/               # Core logic, independent of Discord
│   ├── config.py       #   Settings loaded from .env via pydantic-settings
│   ├── git_manager.py  #   RepositoryManager: clone_or_pull with change detection
│   ├── llm_client.py   #   LLMClient: prompts opencode sessions via HTTP
│   ├── opencode_client.py  # OpencodeClient: HTTP client for opencode serve API
│   ├── opencode_config.py # Setup OpenCode runtime configuration
│   ├── opencode_server.py # OpencodeServer: long-lived opencode serve subprocess
│   ├── rate_limiter.py #   RateLimiter: in-memory sliding-window per-user limiter
│   ├── session_manager.py # SessionManager: per-user session mapping + TTL + locks
│   └── logger.py       #   Shared logging configuration
├── tests/              # pytest test suite (hermetic, no network)
├── data/               # Persistent runtime state (mounted volume in Docker)
│   └── repos/          #   Cloned documentation/source repositories
├── docs/spec/          # Design specification
├── Dockerfile          # Container image definition
├── docker-compose.yml  # Container orchestration
├── .dockerignore       # Files excluded from the Docker build context
├── .env.example        # Template for environment configuration
├── requirements.txt    # Python dependencies
└── main.py             # Entry point: wires components together and runs the bot
```

## Notes

- **Data persistence:** Whether you run via Docker or locally, all persistent state lives under `./data`. Back up this directory to preserve your repository clones.
- **First run:** On first start the bot clones every repository in `REPO_URLS` and generates an `AGENT.md` file for each, which can take a while depending on repository size. Subsequent starts reuse the existing clones.
- **Scope restriction:** The bot is intentionally narrow. Questions unrelated to the indexed plugin receive a polite refusal rather than a best-guess answer.
- **Rate limiting state:** The sliding-window rate limiter is in-memory and per-process; it resets whenever the bot (or container) restarts.
