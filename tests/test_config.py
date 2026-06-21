"""Tests for :mod:`core.config` settings parsing."""

from __future__ import annotations

from pathlib import Path

import pytest
from pydantic import ValidationError

from core.config import Settings, clear_settings_cache, get_settings


def _set_required_env(
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    """Populate the minimum required environment variables hermetically.

    Changes the working directory to an empty ``tmp_path`` so the dev
    ``.env`` file cannot leak into the settings under test, then sets
    the required credentials on the environment directly. The optional
    LLM fields (``LLM_PROVIDER``, ``LLM_BASE_URL``, ``LLM_VARIANT``) are
    explicitly cleared so their defaults are exercised deterministically.

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
    # The LLM endpoint is optional for built-in providers; clear it so
    # the defaults are deterministic even if the host shell sets it.
    monkeypatch.delenv("LLM_BASE_URL", raising=False)
    monkeypatch.delenv("LLM_PROVIDER", raising=False)
    monkeypatch.delenv("LLM_VARIANT", raising=False)


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
    assert settings.LLM_BASE_URL is None
    assert settings.REPO_URLS == ["https://a.git", "https://b.git"]
    assert settings.POLL_INTERVAL_MINUTES == 15
    assert settings.LLM_MODEL == "deepseek-v4-flash-free"


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


def test_settings_default_provider_is_opencode(
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    """``LLM_PROVIDER`` defaults to ``"opencode"`` (OpenCode Zen)."""
    _set_required_env(monkeypatch, tmp_path)
    monkeypatch.setenv("REPO_URLS", "https://a.git")

    clear_settings_cache()
    try:
        settings = get_settings()
    finally:
        clear_settings_cache()

    assert settings.LLM_PROVIDER == "opencode"


def test_settings_custom_provider(
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    """A custom ``LLM_PROVIDER`` is parsed from the environment."""
    _set_required_env(monkeypatch, tmp_path)
    monkeypatch.setenv("REPO_URLS", "https://a.git")
    monkeypatch.setenv("LLM_PROVIDER", "anthropic")

    clear_settings_cache()
    try:
        settings = get_settings()
    finally:
        clear_settings_cache()

    assert settings.LLM_PROVIDER == "anthropic"


def test_settings_variant_default_none(
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    """``LLM_VARIANT`` defaults to ``None`` when unset."""
    _set_required_env(monkeypatch, tmp_path)
    monkeypatch.setenv("REPO_URLS", "https://a.git")

    clear_settings_cache()
    try:
        settings = get_settings()
    finally:
        clear_settings_cache()

    assert settings.LLM_VARIANT is None


def test_settings_variant_custom(
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    """A custom ``LLM_VARIANT`` is parsed from the environment."""
    _set_required_env(monkeypatch, tmp_path)
    monkeypatch.setenv("REPO_URLS", "https://a.git")
    monkeypatch.setenv("LLM_VARIANT", "max")

    clear_settings_cache()
    try:
        settings = get_settings()
    finally:
        clear_settings_cache()

    assert settings.LLM_VARIANT == "max"


def test_settings_base_url_optional(
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    """``LLM_BASE_URL`` is optional for a built-in provider.

    With ``LLM_PROVIDER`` unset (defaulting to ``"opencode"``) and
    ``LLM_BASE_URL`` unset, :class:`Settings` loads successfully and
    ``LLM_BASE_URL`` is ``None``.
    """
    _set_required_env(monkeypatch, tmp_path)
    monkeypatch.setenv("REPO_URLS", "https://a.git")
    monkeypatch.delenv("LLM_BASE_URL", raising=False)

    clear_settings_cache()
    try:
        settings = get_settings()
    finally:
        clear_settings_cache()

    assert settings.LLM_BASE_URL is None
    assert settings.LLM_PROVIDER == "opencode"


def test_settings_base_url_required_for_custom_shim(
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    """``LLM_BASE_URL`` is required when ``LLM_PROVIDER == "custom-llm"``.

    The ``model_validator`` raises ``ValueError`` (surfaced as a
    :class:`pydantic.ValidationError`) when the custom shim is selected
    but no endpoint URL is configured.
    """
    _set_required_env(monkeypatch, tmp_path)
    monkeypatch.setenv("REPO_URLS", "https://a.git")
    monkeypatch.setenv("LLM_PROVIDER", "custom-llm")
    monkeypatch.delenv("LLM_BASE_URL", raising=False)

    clear_settings_cache()
    try:
        with pytest.raises(ValidationError):
            Settings()  # type: ignore[call-arg]
    finally:
        clear_settings_cache()


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
