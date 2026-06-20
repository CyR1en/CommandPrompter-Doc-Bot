"""Tests for :mod:`core.config` settings parsing."""

from __future__ import annotations

from pathlib import Path

import pytest

from core.config import clear_settings_cache, get_settings


def _set_required_env(
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    """Populate the minimum required environment variables hermetically.

    Changes the working directory to an empty ``tmp_path`` so the dev
    ``.env`` file cannot leak into the settings under test, then sets
    the required credentials on the environment directly.

    Args:
        monkeypatch: The pytest monkeypatch fixture.
        tmp_path: A pytest-provided temporary directory with no ``.env``
            file, used as the working directory for the test.
    """
    # Run from an empty directory so the local .env file does not leak
    # values into the settings under test.
    monkeypatch.chdir(tmp_path)
    monkeypatch.setenv("DISCORD_TOKEN", "token")
    monkeypatch.setenv("LLM_API_KEY", "key")
    monkeypatch.setenv("LLM_BASE_URL", "https://example.com/v1")


# ``Path`` is imported lazily below to keep the helper signature readable
# while still satisfying the type checker.
from pathlib import Path  # noqa: E402


def test_settings_loads_required_fields(
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    """Required fields are populated from the environment."""
    _set_required_env(monkeypatch, tmp_path)
    monkeypatch.setenv("REPO_URLS", "https://a.git,https://b.git")
    monkeypatch.setenv("POLL_INTERVAL_MINUTES", "15")

    clear_settings_cache()
    try:
        settings = get_settings()
    finally:
        clear_settings_cache()

    assert settings.DISCORD_TOKEN == "token"
    assert settings.LLM_API_KEY == "key"
    assert settings.LLM_BASE_URL == "https://example.com/v1"
    assert settings.REPO_URLS == ["https://a.git", "https://b.git"]
    assert settings.POLL_INTERVAL_MINUTES == 15
    assert settings.LLM_MODEL == "gpt-3.5-turbo"


def test_settings_custom_models(
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    """A custom LLM model is populated from the environment."""
    _set_required_env(monkeypatch, tmp_path)
    monkeypatch.setenv("REPO_URLS", "https://a.git")
    monkeypatch.setenv("LLM_MODEL", "custom-llm-model")

    clear_settings_cache()
    try:
        settings = get_settings()
    finally:
        clear_settings_cache()

    assert settings.LLM_MODEL == "custom-llm-model"


def test_repo_urls_parses_json_array(
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    """A JSON-encoded array is parsed into a list of URLs."""
    _set_required_env(monkeypatch, tmp_path)
    monkeypatch.setenv(
        "REPO_URLS", '["https://a.git", "https://b.git"]'
    )

    clear_settings_cache()
    try:
        settings = get_settings()
    finally:
        clear_settings_cache()

    assert settings.REPO_URLS == ["https://a.git", "https://b.git"]


def test_repo_urls_empty_yields_empty_list(
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    """An empty ``REPO_URLS`` value yields an empty list."""
    _set_required_env(monkeypatch, tmp_path)
    monkeypatch.setenv("REPO_URLS", "")

    clear_settings_cache()
    try:
        settings = get_settings()
    finally:
        clear_settings_cache()

    assert settings.REPO_URLS == []


def test_poll_interval_defaults_to_ten(
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    """``POLL_INTERVAL_MINUTES`` defaults to 10 when omitted."""
    _set_required_env(monkeypatch, tmp_path)
    monkeypatch.setenv("REPO_URLS", "https://a.git")
    monkeypatch.delenv("POLL_INTERVAL_MINUTES", raising=False)

    clear_settings_cache()
    try:
        settings = get_settings()
    finally:
        clear_settings_cache()

    assert settings.POLL_INTERVAL_MINUTES == 10


def test_settings_github_token_default(
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    """GITHUB_TOKEN defaults to None when omitted."""
    _set_required_env(monkeypatch, tmp_path)
    monkeypatch.setenv("REPO_URLS", "https://a.git")
    monkeypatch.delenv("GITHUB_TOKEN", raising=False)

    clear_settings_cache()
    try:
        settings = get_settings()
    finally:
        clear_settings_cache()

    assert settings.GITHUB_TOKEN is None


def test_settings_github_token_custom(
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    """GITHUB_TOKEN is parsed from the environment."""
    _set_required_env(monkeypatch, tmp_path)
    monkeypatch.setenv("REPO_URLS", "https://a.git")
    monkeypatch.setenv("GITHUB_TOKEN", "my-github-token")

    clear_settings_cache()
    try:
        settings = get_settings()
    finally:
        clear_settings_cache()

    assert settings.GITHUB_TOKEN == "my-github-token"
