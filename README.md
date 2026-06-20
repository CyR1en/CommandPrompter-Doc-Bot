# CMDP Doc Bot

A Discord bot that answers questions about a Minecraft plugin using the **OpenCode CLI** over locally cloned documentation and source code repositories.

The bot maintains its own working copies of the target Git repositories and periodically polls for upstream changes so its answers always reflect the latest documentation. When a user `@mentions` the bot in a Discord channel, it spawns an `opencode` subprocess configured with a strict system prompt to search the cloned repositories, read relevant files, and reply with a grounded, scope-locked answer.

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

1. **Startup** — The bot loads settings from `.env`, dynamically generates its own `opencode.json` configuration, copies the `docbot` agent persona into `~/.config/opencode/agents/`, and wires together the repository manager, rate limiter, LLM client, and Discord client.
2. **Polling & Preprocessing** — A background task (`discord.ext.tasks.loop`) runs every `POLL_INTERVAL_MINUTES` minutes, pulls each repository into `data/repos/<name>`, and spawns an `opencode` subprocess to generate an `AGENT.md` file in the root of any repository that was updated.
3. **Answering** — On an `@mention`, the bot strips its mention from the message, checks the user's rate limit, and spawns `opencode run --agent docbot --format json --dir data/repos "<query>"`. It concatenates the `text` events from stdout and returns the final answer.

## Prerequisites

- **Docker** and **Docker Compose** (recommended) — any recent version with the Compose v2 plugin (`docker compose`).
  - Verify with: `docker --version && docker compose version`
- **Python 3.11+** — only required if you want to run the bot or the test suite locally without Docker.
- **OpenCode CLI** — required to be installed and available on the system `PATH` if running locally.
- A **Discord bot token** with the **Message Content** privileged intent enabled (see [Discord Developer Portal](https://discord.com/developers/applications)).
- An **OpenAI-compatible LLM endpoint** (base URL + API key).

## Configuration (`.env`)

All runtime configuration is supplied through environment variables, loaded from a `.env` file by `pydantic-settings`. Copy the provided template and fill in your values:

```bash
cp .env.example .env
```

`.env` fields:

| Variable                | Required | Default | Description                                                                                                              |
| ----------------------- | -------- | ------- | ------------------------------------------------------------------------------------------------------------------------ |
| `DISCORD_TOKEN`         | yes      | —       | Authentication token for the Discord bot account.                                                                        |
| `LLM_API_KEY`           | yes      | —       | API key used to authenticate with the OpenAI-compatible LLM provider.                                                    |
| `LLM_BASE_URL`          | yes      | —       | Base URL of the OpenAI-compatible LLM endpoint (e.g. `https://api.example.com/v1`).                                      |
| `LLM_MODEL`             | no       | `gpt-3.5-turbo` | Model name to use for LLM chat generation.                                                                               |
| `GITHUB_TOKEN`          | no       | —       | GitHub token used to authenticate when cloning/pulling private repositories. Automatically injected into `https://github.com/...` URLs. |
| `REPO_URLS`             | yes      | —       | Git repositories to clone and search. Accepts a comma-separated list **or** a JSON array (see examples below).           |
| `POLL_INTERVAL_MINUTES` | no       | `10`    | Minutes between repository polling cycles.                                                                               |

`REPO_URLS` examples — both forms are valid:

```dotenv
# Comma-separated
REPO_URLS=https://github.com/example/plugin-docs.git,https://github.com/example/plugin-source.git

# JSON array
REPO_URLS=["https://github.com/example/plugin-docs.git","https://github.com/example/plugin-source.git"]
```

> **Tip:** The `.env` file is read by both the local run and Docker Compose (via the `env_file` directive). It is listed in `.dockerignore` so secrets are never baked into the image.

### Discord intents

The bot requires the **Message Content** privileged intent to read `@mention` questions. Enable it on your application's **Bot** page in the Discord Developer Portal before running.

## Running with Docker Compose

This is the recommended way to run the bot. Docker Compose builds the image from the local `Dockerfile`, mounts `./data` for persistent state, and injects configuration from `.env`.

1. **Configure the environment** (see [Configuration](#configuration-env)):

   ```bash
   cp .env.example .env
   # edit .env and fill in DISCORD_TOKEN, LLM_API_KEY, LLM_BASE_URL, REPO_URLS
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
│   ├── llm_client.py   #   LLMClient: spawns opencode to answer questions
│   ├── opencode_config.py # Setup OpenCode runtime configuration
│   ├── rate_limiter.py #   RateLimiter: in-memory sliding-window per-user limiter
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
