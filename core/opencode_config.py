"""OpenCode configuration generator for the CMDP Doc Bot.

This module provides :func:`setup_opencode`, which writes the OpenCode
agent orchestrator's runtime configuration to ``~/.config/opencode/`` so
the bot can spawn ``opencode run`` subprocesses that answer user
questions against the cloned repositories.

The generated ``opencode.json`` defines a custom OpenAI-compatible
provider wired to the bot's ``LLM_BASE_URL``, ``LLM_API_KEY``, and
``LLM_MODEL`` settings, plus a ``docbot`` agent entry that runs in
primary mode with ``read``/``grep``/``list``/``bash`` permissions. The
agent persona (``agent/docbot.md``) is copied into
``<config_dir>/agents/docbot.md`` so OpenCode picks it up as the agent's
system prompt.
"""

from __future__ import annotations

import json
import shutil
from pathlib import Path

from core.config import Settings
from core.logger import get_logger

_logger = get_logger(__name__)

#: Default filesystem location of the OpenCode per-user config directory.
DEFAULT_CONFIG_DIR: Path = Path.home() / ".config" / "opencode"

#: Default filesystem location of the agent persona source file shipped
#: with the bot, which is copied into OpenCode's agents directory.
DEFAULT_AGENT_SOURCE: Path = Path("agent/docbot.md")

#: Name of the custom LLM provider entry written to ``opencode.json``.
_PROVIDER_NAME: str = "custom-llm"

#: Name of the OpenCode agent entry written to ``opencode.json``.
_AGENT_NAME: str = "docbot"


def setup_opencode(
    settings: Settings,
    *,
    config_dir: Path | None = None,
    agent_source: Path = DEFAULT_AGENT_SOURCE,
) -> None:
    """Generate OpenCode's runtime configuration from the bot settings.

    Writes ``opencode.json`` into the OpenCode config directory
    (``~/.config/opencode`` by default) defining a custom
    OpenAI-compatible provider wired to the bot's ``LLM_BASE_URL``,
    ``LLM_API_KEY``, and ``LLM_MODEL`` settings, plus a ``docbot`` agent
    entry that runs in primary mode with ``read``/``grep``/``list``/
    ``bash`` permissions. Also copies the agent persona file
    (``agent/docbot.md`` by default) into ``<config_dir>/agents/docbot.md``
    so OpenCode picks it up as the agent's system prompt.

    Args:
        settings: The application :class:`Settings` carrying the LLM
            endpoint credentials and model name.
        config_dir: Target OpenCode config directory. Defaults to
            :data:`DEFAULT_CONFIG_DIR` (``~/.config/opencode``). Override
            in tests to point at a temporary directory.
        agent_source: Path to the agent persona Markdown file to copy
            into ``<config_dir>/agents/docbot.md``. Defaults to
            :data:`DEFAULT_AGENT_SOURCE`.

    Raises:
        FileNotFoundError: If ``agent_source`` does not exist.
    """
    root: Path = (
        Path(config_dir) if config_dir is not None else DEFAULT_CONFIG_DIR
    )
    agents_dir: Path = root / "agents"
    config_path: Path = root / "opencode.json"
    agent_target: Path = agents_dir / "docbot.md"

    root.mkdir(parents=True, exist_ok=True)
    agents_dir.mkdir(parents=True, exist_ok=True)

    config: dict[str, object] = {
        "provider": {
            _PROVIDER_NAME: {
                "npm": "@ai-sdk/openai-compatible",
                "name": "Custom LLM",
                "options": {
                    "baseURL": settings.LLM_BASE_URL,
                    "apiKey": settings.LLM_API_KEY,
                },
                "models": {
                    settings.LLM_MODEL: {
                        "name": "Configured Model",
                    },
                },
            },
        },
        "agent": {
            _AGENT_NAME: {
                "mode": "primary",
                "model": f"{_PROVIDER_NAME}/{settings.LLM_MODEL}",
                "variant": "max",
                "permission": {
                    "read": "allow",
                    "grep": "allow",
                    "list": "allow",
                    "bash": "allow",
                },
            },
        },
    }

    config_path.write_text(
        json.dumps(config, indent=2),
        encoding="utf-8",
    )
    _logger.info("Wrote OpenCode config to %s", config_path)

    if not agent_source.exists():
        raise FileNotFoundError(
            f"Agent persona source not found: {agent_source}"
        )

    shutil.copyfile(agent_source, agent_target)
    _logger.info(
        "Copied agent persona %s -> %s", agent_source, agent_target
    )
