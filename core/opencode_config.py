"""OpenCode configuration generator for the CMDP Doc Bot.

This module provides :func:`setup_opencode`, which writes the OpenCode
agent orchestrator's runtime configuration to ``~/.config/opencode/`` so
the bot can spawn ``opencode run`` subprocesses that answer user
questions against the cloned repositories.

The generated ``opencode.json`` is intentionally minimal: it only
contains the ``docbot`` agent entry (running in primary mode with
``read``/``grep``/``list``/``bash`` permissions) whose ``model`` is
``"<LLM_PROVIDER>/<LLM_MODEL>"`` and whose optional ``variant`` is taken
from ``LLM_VARIANT``. The provider itself is resolved from OpenCode's
built-in catalog (loaded at runtime from
https://models.dev/api.json) — no ``provider`` block is written for
built-in providers like ``opencode`` (OpenCode Zen), ``anthropic``,
``openai``, etc.

For the legacy ``custom-llm`` shim (``LLM_PROVIDER == "custom-llm"`` with
``LLM_BASE_URL`` set), a custom OpenAI-compatible ``provider`` block is
written exactly as before so the bot can target an arbitrary
OpenAI-compatible endpoint.

The agent persona (``agent/docbot.md``) is copied into
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

#: Name of the legacy custom OpenAI-compatible provider entry written to
#: ``opencode.json``. Only used when ``LLM_PROVIDER == "custom-llm"``;
#: every other provider ID is resolved from OpenCode's built-in catalog
#: (https://models.dev/api.json) and no ``provider`` block is written.
_CUSTOM_PROVIDER_NAME: str = "custom-llm"

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
    (``~/.config/opencode`` by default) containing a ``docbot`` agent
    entry that runs in primary mode with ``read``/``grep``/``list``/
    ``bash`` permissions. The agent's ``model`` is set to
    ``"<LLM_PROVIDER>/<LLM_MODEL>"`` and its ``variant`` is set to
    ``LLM_VARIANT`` when that is not ``None``.

    Provider resolution is dual-mode:

    * **Built-in providers** (the default, e.g. ``opencode`` / OpenCode
      Zen, ``anthropic``, ``openai``): the provider is auto-resolved from
      OpenCode's built-in catalog (loaded from
      https://models.dev/api.json) and NO ``provider`` block is written
      to ``opencode.json``. The provider's API key is expected to be
      supplied via its declared env var (e.g. ``OPENCODE_API_KEY``),
      which :mod:`main` sets up at startup.

    * **Legacy custom shim** (``LLM_PROVIDER == "custom-llm"`` with
      ``LLM_BASE_URL`` set): a custom OpenAI-compatible ``provider``
      block is written exactly as before, wired to the bot's
      ``LLM_BASE_URL``, ``LLM_API_KEY``, and ``LLM_MODEL`` settings. If
      ``LLM_PROVIDER == "custom-llm"`` but ``LLM_BASE_URL`` is unset the
      ``provider`` block is omitted and a warning is logged (the
      resulting config is invalid, which is preferable to silently
      writing a broken provider block).

    The agent persona file (``agent/docbot.md`` by default) is copied
    into ``<config_dir>/agents/docbot.md`` so OpenCode picks it up as
    the agent's system prompt.

    Args:
        settings: The application :class:`Settings` carrying the LLM
            provider ID, model name, optional variant, and (for the
            custom shim) endpoint credentials.
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

    agent_model: str = f"{settings.LLM_PROVIDER}/{settings.LLM_MODEL}"

    agent_entry: dict[str, object] = {
        "mode": "primary",
        "model": agent_model,
        "permission": {
            "read": "allow",
            "grep": "allow",
            "list": "allow",
            "bash": "allow",
        },
    }
    # Only include ``variant`` when explicitly set so the JSON does not
    # carry a null field (which OpenCode would interpret as "use no
    # variant" rather than "use the model default").
    if settings.LLM_VARIANT is not None:
        agent_entry["variant"] = settings.LLM_VARIANT

    config: dict[str, object] = {
        "agent": {
            _AGENT_NAME: agent_entry,
        },
    }

    # Only write a ``provider`` block for the legacy custom shim, and
    # only when a base URL is configured. Built-in providers
    # (opencode/anthropic/openai/...) auto-resolve from OpenCode's
    # built-in catalog (https://models.dev/api.json), so writing a
    # provider block would shadow their model presets, capabilities,
    # and cost metadata.
    provider_block_written: bool = False
    if (
        settings.LLM_PROVIDER == _CUSTOM_PROVIDER_NAME
        and settings.LLM_BASE_URL
    ):
        config["provider"] = {
            _CUSTOM_PROVIDER_NAME: {
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
        }
        provider_block_written = True
    elif settings.LLM_PROVIDER == _CUSTOM_PROVIDER_NAME:
        _logger.warning(
            "LLM_PROVIDER is 'custom-llm' but LLM_BASE_URL is unset; "
            "omitting the provider block from %s (the opencode config "
            "will be invalid). Set LLM_BASE_URL or switch LLM_PROVIDER "
            "to a built-in provider such as 'opencode'.",
            config_path,
        )

    config_path.write_text(
        json.dumps(config, indent=2),
        encoding="utf-8",
    )
    _logger.info("Wrote OpenCode config to %s", config_path)
    _logger.info(
        "Configured docbot agent: model=%s variant=%s provider_block=%s",
        agent_model,
        settings.LLM_VARIANT
        if settings.LLM_VARIANT is not None
        else "default",
        "written" if provider_block_written else "omitted",
    )

    if not agent_source.exists():
        raise FileNotFoundError(
            f"Agent persona source not found: {agent_source}"
        )

    shutil.copyfile(agent_source, agent_target)
    _logger.info(
        "Copied agent persona %s -> %s", agent_source, agent_target
    )
