"""Tests for :mod:`core.opencode_config`.

The tests point :func:`setup_opencode` at a temporary config directory and
a temporary agent persona source so they exercise the real filesystem
writes without touching the user's ``~/.config/opencode`` directory.

Both configuration modes are covered:

* The legacy ``custom-llm`` shim (``LLM_PROVIDER == "custom-llm"`` with
  ``LLM_BASE_URL`` set) writes a custom OpenAI-compatible ``provider``
  block into ``opencode.json``.
* Built-in providers (``opencode``, ``anthropic``, ...) omit the
  ``provider`` block entirely so OpenCode auto-resolves the provider
  from its built-in catalog (https://models.dev/api.json).
"""

from __future__ import annotations

import json
from pathlib import Path
from unittest.mock import MagicMock

import pytest

from core.opencode_config import (
    _AGENT_NAME,
    _CUSTOM_PROVIDER_NAME,
    setup_opencode,
)


def _make_settings(
    *,
    provider: str = "custom-llm",
    base_url: str | None = None,
    api_key: str = "secret-key",
    model: str = "test-model",
    variant: str | None = None,
) -> MagicMock:
    """Build a fake :class:`Settings` with the given LLM configuration.

    Args:
        provider: The ``LLM_PROVIDER`` value to expose. Defaults to
            ``"custom-llm"`` (the legacy shim) so tests that only care
            about structural writes keep their historical behavior.
        base_url: The ``LLM_BASE_URL`` value to expose. Defaults to
            ``None``; callers exercising the custom shim must pass an
            explicit URL.
        api_key: The ``LLM_API_KEY`` value to expose.
        model: The ``LLM_MODEL`` value to expose.
        variant: The ``LLM_VARIANT`` value to expose. Defaults to
            ``None`` (no variant key written).

    Returns:
        A :class:`MagicMock` with the LLM attributes set. Only the
        attributes read by :func:`setup_opencode` are populated.
    """
    settings = MagicMock(name="settings")
    settings.LLM_PROVIDER = provider
    settings.LLM_BASE_URL = base_url
    settings.LLM_API_KEY = api_key
    settings.LLM_MODEL = model
    settings.LLM_VARIANT = variant
    return settings


# ---------------------------------------------------------------------------
# Legacy custom-llm shim
# ---------------------------------------------------------------------------


def test_setup_writes_custom_provider_block_when_provider_is_custom_llm(
    tmp_path: Path,
) -> None:
    """The custom ``provider`` block is written for the legacy shim.

    With ``LLM_PROVIDER == "custom-llm"`` and ``LLM_BASE_URL`` set,
    ``opencode.json`` defines the custom OpenAI-compatible provider
    wired to the bot's credentials, exactly as before the migration.
    """
    settings = _make_settings(
        provider="custom-llm",
        base_url="https://llm.example.com/v1",
    )
    agent_src = tmp_path / "source" / "docbot.md"
    agent_src.parent.mkdir(parents=True)
    agent_src.write_text("# persona", encoding="utf-8")

    config_dir = tmp_path / "opencode"
    setup_opencode(settings, config_dir=config_dir, agent_source=agent_src)

    config_path = config_dir / "opencode.json"
    assert config_path.is_file()
    config = json.loads(config_path.read_text(encoding="utf-8"))

    provider = config["provider"][_CUSTOM_PROVIDER_NAME]
    assert provider["npm"] == "@ai-sdk/openai-compatible"
    assert provider["name"] == "Custom LLM"
    assert provider["options"]["baseURL"] == "https://llm.example.com/v1"
    assert provider["options"]["apiKey"] == "secret-key"
    assert provider["models"]["test-model"]["name"] == "Configured Model"


def test_setup_writes_opencode_json_with_agent(
    tmp_path: Path,
) -> None:
    """The ``docbot`` agent is written in primary mode with the shim model.

    For the custom shim the agent's ``model`` is
    ``"custom-llm/<LLM_MODEL>"`` and no ``variant`` key is present when
    ``LLM_VARIANT`` is unset.
    """
    settings = _make_settings(
        provider="custom-llm",
        base_url="https://llm.example.com/v1",
        model="my-model",
    )
    agent_src = tmp_path / "docbot.md"
    agent_src.write_text("persona", encoding="utf-8")

    config_dir = tmp_path / "opencode"
    setup_opencode(settings, config_dir=config_dir, agent_source=agent_src)

    config = json.loads(
        (config_dir / "opencode.json").read_text(encoding="utf-8")
    )

    agent = config["agent"][_AGENT_NAME]
    assert agent["mode"] == "primary"
    assert agent["model"] == f"{_CUSTOM_PROVIDER_NAME}/my-model"
    perms = agent["permission"]
    assert perms["read"] == "allow"
    assert perms["grep"] == "allow"
    assert perms["list"] == "allow"
    assert perms["bash"] == "allow"


def test_setup_copies_agent_persona(tmp_path: Path) -> None:
    """The agent persona source is copied to ``agents/docbot.md``."""
    settings = _make_settings(
        provider="custom-llm", base_url="https://llm.example.com/v1"
    )
    agent_src = tmp_path / "source.md"
    agent_src.write_text("PERSONA BODY", encoding="utf-8")

    config_dir = tmp_path / "opencode"
    setup_opencode(settings, config_dir=config_dir, agent_source=agent_src)

    copied = config_dir / "agents" / "docbot.md"
    assert copied.is_file()
    assert copied.read_text(encoding="utf-8") == "PERSONA BODY"


def test_setup_creates_config_and_agents_dirs(tmp_path: Path) -> None:
    """Missing config and agents directories are created on demand."""
    settings = _make_settings(
        provider="custom-llm", base_url="https://llm.example.com/v1"
    )
    agent_src = tmp_path / "a.md"
    agent_src.write_text("x", encoding="utf-8")

    config_dir = tmp_path / "nested" / "opencode"
    assert not config_dir.exists()

    setup_opencode(settings, config_dir=config_dir, agent_source=agent_src)

    assert config_dir.is_dir()
    assert (config_dir / "agents").is_dir()


def test_setup_raises_on_missing_agent_source(tmp_path: Path) -> None:
    """A missing agent persona source raises ``FileNotFoundError``."""
    settings = _make_settings(
        provider="custom-llm", base_url="https://llm.example.com/v1"
    )
    config_dir = tmp_path / "opencode"
    missing = tmp_path / "does_not_exist.md"

    with pytest.raises(FileNotFoundError):
        setup_opencode(settings, config_dir=config_dir, agent_source=missing)


def test_setup_overwrites_existing_config(tmp_path: Path) -> None:
    """A second call overwrites the previous ``opencode.json`` content."""
    settings = _make_settings(
        provider="custom-llm",
        base_url="https://llm.example.com/v1",
        model="v1",
    )
    agent_src = tmp_path / "a.md"
    agent_src.write_text("p", encoding="utf-8")

    config_dir = tmp_path / "opencode"
    setup_opencode(settings, config_dir=config_dir, agent_source=agent_src)

    settings2 = _make_settings(
        provider="custom-llm",
        base_url="https://llm.example.com/v1",
        model="v2",
    )
    setup_opencode(settings2, config_dir=config_dir, agent_source=agent_src)

    config = json.loads(
        (config_dir / "opencode.json").read_text(encoding="utf-8")
    )
    agent_model = config["agent"][_AGENT_NAME]["model"]
    assert agent_model == f"{_CUSTOM_PROVIDER_NAME}/v2"


def test_setup_json_is_valid_and_indented(tmp_path: Path) -> None:
    """The written ``opencode.json`` is valid JSON with indentation."""
    settings = _make_settings(
        provider="custom-llm", base_url="https://llm.example.com/v1"
    )
    agent_src = tmp_path / "a.md"
    agent_src.write_text("p", encoding="utf-8")

    config_dir = tmp_path / "opencode"
    setup_opencode(settings, config_dir=config_dir, agent_source=agent_src)

    raw = (config_dir / "opencode.json").read_text(encoding="utf-8")
    # Should be parseable (valid JSON).
    json.loads(raw)
    # Should contain indentation (pretty-printed).
    assert "\n" in raw
    assert '  "' in raw


# ---------------------------------------------------------------------------
# Built-in providers (the migration default)
# ---------------------------------------------------------------------------


def test_setup_omits_provider_block_for_builtin_provider(
    tmp_path: Path,
) -> None:
    """No ``provider`` key is written for a built-in provider.

    OpenCode auto-resolves built-in providers (loaded from
    https://models.dev/api.json) so writing a ``provider`` block would
    shadow their model presets, capabilities, and cost metadata.
    """
    settings = _make_settings(provider="opencode", base_url=None)
    agent_src = tmp_path / "a.md"
    agent_src.write_text("p", encoding="utf-8")

    config_dir = tmp_path / "opencode"
    setup_opencode(settings, config_dir=config_dir, agent_source=agent_src)

    config = json.loads(
        (config_dir / "opencode.json").read_text(encoding="utf-8")
    )
    assert "provider" not in config
    # The agent block is still present.
    assert _AGENT_NAME in config["agent"]


def test_setup_writes_agent_with_builtin_model_reference(
    tmp_path: Path,
) -> None:
    """The agent ``model`` is ``"<provider>/<model>"`` for built-ins."""
    settings = _make_settings(
        provider="opencode",
        base_url=None,
        model="deepseek-v4-flash-free",
    )
    agent_src = tmp_path / "a.md"
    agent_src.write_text("p", encoding="utf-8")

    config_dir = tmp_path / "opencode"
    setup_opencode(settings, config_dir=config_dir, agent_source=agent_src)

    config = json.loads(
        (config_dir / "opencode.json").read_text(encoding="utf-8")
    )
    assert config["agent"][_AGENT_NAME]["model"] == (
        "opencode/deepseek-v4-flash-free"
    )
    assert "provider" not in config


def test_setup_writes_variant_when_set(tmp_path: Path) -> None:
    """The agent ``variant`` key is written when ``LLM_VARIANT`` is set."""
    settings = _make_settings(
        provider="opencode",
        base_url=None,
        model="deepseek-v4-flash-free",
        variant="max",
    )
    agent_src = tmp_path / "a.md"
    agent_src.write_text("p", encoding="utf-8")

    config_dir = tmp_path / "opencode"
    setup_opencode(settings, config_dir=config_dir, agent_source=agent_src)

    config = json.loads(
        (config_dir / "opencode.json").read_text(encoding="utf-8")
    )
    assert config["agent"][_AGENT_NAME]["variant"] == "max"


def test_setup_omits_variant_when_none(tmp_path: Path) -> None:
    """No ``variant`` key is written when ``LLM_VARIANT`` is ``None``.

    This avoids serializing a null field, which OpenCode would interpret
    as "use no variant" rather than "use the model default".
    """
    settings = _make_settings(
        provider="opencode",
        base_url=None,
        model="deepseek-v4-flash-free",
        variant=None,
    )
    agent_src = tmp_path / "a.md"
    agent_src.write_text("p", encoding="utf-8")

    config_dir = tmp_path / "opencode"
    setup_opencode(settings, config_dir=config_dir, agent_source=agent_src)

    config = json.loads(
        (config_dir / "opencode.json").read_text(encoding="utf-8")
    )
    assert "variant" not in config["agent"][_AGENT_NAME]


def test_setup_supports_anthropic_builtin(tmp_path: Path) -> None:
    """The built-in ``anthropic`` provider is wired without a provider block."""
    settings = _make_settings(
        provider="anthropic",
        base_url=None,
        model="claude-sonnet-4-5",
    )
    agent_src = tmp_path / "a.md"
    agent_src.write_text("p", encoding="utf-8")

    config_dir = tmp_path / "opencode"
    setup_opencode(settings, config_dir=config_dir, agent_source=agent_src)

    config = json.loads(
        (config_dir / "opencode.json").read_text(encoding="utf-8")
    )
    assert config["agent"][_AGENT_NAME]["model"] == (
        "anthropic/claude-sonnet-4-5"
    )
    assert "provider" not in config


def test_setup_custom_provider_block_requires_base_url(
    tmp_path: Path,
    caplog: pytest.LogCaptureFixture,
) -> None:
    """The ``provider`` block is omitted when ``custom-llm`` lacks a base URL.

    With ``LLM_PROVIDER == "custom-llm"`` but ``LLM_BASE_URL is None``
    the provider block is omitted (rather than silently writing a broken
    provider block) and a warning is logged. The resulting
    ``opencode.json`` is invalid for the custom shim, which surfaces the
    misconfiguration instead of hiding it.
    """
    import logging

    settings = _make_settings(provider="custom-llm", base_url=None)
    agent_src = tmp_path / "a.md"
    agent_src.write_text("p", encoding="utf-8")

    config_dir = tmp_path / "opencode"
    caplog.set_level(logging.WARNING, logger="core.opencode_config")
    setup_opencode(settings, config_dir=config_dir, agent_source=agent_src)

    config = json.loads(
        (config_dir / "opencode.json").read_text(encoding="utf-8")
    )
    assert "provider" not in config
    assert any(
        "LLM_BASE_URL" in record.message for record in caplog.records
    )
