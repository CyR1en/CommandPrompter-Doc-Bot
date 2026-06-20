"""Tests for :mod:`core.opencode_config`.

The tests point :func:`setup_opencode` at a temporary config directory and
a temporary agent persona source so they exercise the real filesystem
writes without touching the user's ``~/.config/opencode`` directory.
"""

from __future__ import annotations

import json
from pathlib import Path
from unittest.mock import MagicMock

import pytest

from core.opencode_config import (
    _AGENT_NAME,
    _PROVIDER_NAME,
    setup_opencode,
)


def _make_settings(
    *,
    base_url: str = "https://llm.example.com/v1",
    api_key: str = "secret-key",
    model: str = "test-model",
) -> MagicMock:
    """Build a fake :class:`Settings` with the given LLM credentials.

    Args:
        base_url: The ``LLM_BASE_URL`` value to expose.
        api_key: The ``LLM_API_KEY`` value to expose.
        model: The ``LLM_MODEL`` value to expose.

    Returns:
        A :class:`MagicMock` with the three LLM attributes set. Only the
        attributes read by :func:`setup_opencode` are populated.
    """
    settings = MagicMock(name="settings")
    settings.LLM_BASE_URL = base_url
    settings.LLM_API_KEY = api_key
    settings.LLM_MODEL = model
    return settings


def test_setup_writes_opencode_json_with_provider(
    tmp_path: Path,
) -> None:
    """``opencode.json`` defines the custom LLM provider from settings."""
    settings = _make_settings()
    agent_src = tmp_path / "source" / "docbot.md"
    agent_src.parent.mkdir(parents=True)
    agent_src.write_text("# persona", encoding="utf-8")

    config_dir = tmp_path / "opencode"
    setup_opencode(settings, config_dir=config_dir, agent_source=agent_src)

    config_path = config_dir / "opencode.json"
    assert config_path.is_file()
    config = json.loads(config_path.read_text(encoding="utf-8"))

    provider = config["provider"][_PROVIDER_NAME]
    assert provider["npm"] == "@ai-sdk/openai-compatible"
    assert provider["name"] == "Custom LLM"
    assert provider["options"]["baseURL"] == "https://llm.example.com/v1"
    assert provider["options"]["apiKey"] == "secret-key"
    assert provider["models"]["test-model"]["name"] == "Configured Model"


def test_setup_writes_opencode_json_with_agent(
    tmp_path: Path,
) -> None:
    """``opencode.json`` defines the ``docbot`` agent in primary mode."""
    settings = _make_settings(model="my-model")
    agent_src = tmp_path / "docbot.md"
    agent_src.write_text("persona", encoding="utf-8")

    config_dir = tmp_path / "opencode"
    setup_opencode(settings, config_dir=config_dir, agent_source=agent_src)

    config = json.loads(
        (config_dir / "opencode.json").read_text(encoding="utf-8")
    )

    agent = config["agent"][_AGENT_NAME]
    assert agent["mode"] == "primary"
    assert agent["model"] == f"{_PROVIDER_NAME}/my-model"
    perms = agent["permission"]
    assert perms["read"] == "allow"
    assert perms["grep"] == "allow"
    assert perms["list"] == "allow"
    assert perms["bash"] == "allow"


def test_setup_copies_agent_persona(tmp_path: Path) -> None:
    """The agent persona source is copied to ``agents/docbot.md``."""
    settings = _make_settings()
    agent_src = tmp_path / "source.md"
    agent_src.write_text("PERSONA BODY", encoding="utf-8")

    config_dir = tmp_path / "opencode"
    setup_opencode(settings, config_dir=config_dir, agent_source=agent_src)

    copied = config_dir / "agents" / "docbot.md"
    assert copied.is_file()
    assert copied.read_text(encoding="utf-8") == "PERSONA BODY"


def test_setup_creates_config_and_agents_dirs(tmp_path: Path) -> None:
    """Missing config and agents directories are created on demand."""
    settings = _make_settings()
    agent_src = tmp_path / "a.md"
    agent_src.write_text("x", encoding="utf-8")

    config_dir = tmp_path / "nested" / "opencode"
    assert not config_dir.exists()

    setup_opencode(settings, config_dir=config_dir, agent_source=agent_src)

    assert config_dir.is_dir()
    assert (config_dir / "agents").is_dir()


def test_setup_raises_on_missing_agent_source(tmp_path: Path) -> None:
    """A missing agent persona source raises ``FileNotFoundError``."""
    settings = _make_settings()
    config_dir = tmp_path / "opencode"
    missing = tmp_path / "does_not_exist.md"

    with pytest.raises(FileNotFoundError):
        setup_opencode(settings, config_dir=config_dir, agent_source=missing)


def test_setup_overwrites_existing_config(tmp_path: Path) -> None:
    """A second call overwrites the previous ``opencode.json`` content."""
    settings = _make_settings(model="v1")
    agent_src = tmp_path / "a.md"
    agent_src.write_text("p", encoding="utf-8")

    config_dir = tmp_path / "opencode"
    setup_opencode(settings, config_dir=config_dir, agent_source=agent_src)

    settings2 = _make_settings(model="v2")
    setup_opencode(settings2, config_dir=config_dir, agent_source=agent_src)

    config = json.loads(
        (config_dir / "opencode.json").read_text(encoding="utf-8")
    )
    agent_model = config["agent"][_AGENT_NAME]["model"]
    assert agent_model == f"{_PROVIDER_NAME}/v2"


def test_setup_json_is_valid_and_indented(tmp_path: Path) -> None:
    """The written ``opencode.json`` is valid JSON with indentation."""
    settings = _make_settings()
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
