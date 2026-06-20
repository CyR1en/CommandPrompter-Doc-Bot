"""Application configuration loaded from environment variables.

This module defines the :class:`Settings` class, which uses
``pydantic-settings`` to load and validate configuration from environment
variables (and an optional ``.env`` file).
"""

from __future__ import annotations

import json
from collections.abc import Sequence
from functools import lru_cache
from typing import Annotated

from pydantic import field_validator
from pydantic_settings import BaseSettings, NoDecode, SettingsConfigDict


class Settings(BaseSettings):
    """Runtime configuration for the CMDP Doc Bot.

    Settings are populated from environment variables (or a local
    ``.env`` file). The required fields must be provided before the bot
    can fully start.

    Attributes:
        DISCORD_TOKEN: Authentication token for the Discord bot account.
        LLM_API_KEY: API key used to authenticate with the LLM provider.
        LLM_BASE_URL: Base URL of the OpenAI-compatible LLM endpoint.
        LLM_MODEL: The model name to use for LLM chat generation.
        GITHUB_TOKEN: Optional GitHub token used to authenticate when
            cloning/pulling private repositories.
        REPO_URLS: Git repository URLs to clone and compile for the
            full-context documentation. May be supplied as a JSON-encoded
            list or a comma-separated string.
        POLL_INTERVAL_MINUTES: Interval, in minutes, between repository
            polling for upstream changes.
    """

    model_config = SettingsConfigDict(
        env_file=".env",
        env_file_encoding="utf-8",
        case_sensitive=True,
        extra="ignore",
    )

    DISCORD_TOKEN: str
    LLM_API_KEY: str
    LLM_BASE_URL: str
    LLM_MODEL: str = "gpt-3.5-turbo"
    GITHUB_TOKEN: str | None = None
    # ``NoDecode`` tells pydantic-settings to pass the raw environment value
    # through to the ``_split_repo_urls`` validator instead of trying to
    # JSON-decode it first, which lets us accept a comma-separated string.
    REPO_URLS: Annotated[list[str], NoDecode]
    POLL_INTERVAL_MINUTES: int = 10

    @field_validator("REPO_URLS", mode="before")
    @classmethod
    def _split_repo_urls(cls, value: object) -> list[str]:
        """Parse ``REPO_URLS`` into a list of URL strings.

        Accepts a JSON-encoded list, a comma-separated string, or a
        pre-parsed list/tuple. Whitespace is stripped from each entry and
        empty entries are dropped.

        Args:
            value: The raw value provided by the environment or ``.env``
                file.

        Returns:
            A list of non-empty repository URL strings.

        Raises:
            ValueError: If the value cannot be coerced into a list of
                strings, or if a JSON payload does not encode a list.
        """
        if value is None or value == "":
            return []

        candidates: Sequence[object]
        if isinstance(value, str):
            stripped = value.strip()
            if stripped.startswith("["):
                parsed = json.loads(stripped)
                if not isinstance(parsed, list):
                    raise ValueError(
                        "REPO_URLS JSON payload must encode a list"
                    )
                candidates = list(parsed)
            else:
                candidates = stripped.split(",")
        elif isinstance(value, (list, tuple)):
            candidates = list(value)
        else:
            raise ValueError(
                "REPO_URLS must be a comma-separated string or a list"
            )

        return [str(item).strip() for item in candidates if str(item).strip()]


@lru_cache(maxsize=1)
def get_settings() -> Settings:
    """Return a cached :class:`Settings` instance.

    The result is cached so repeated calls reuse a single parsed
    configuration object. Call :func:`clear_settings_cache` to force a
    reload, e.g. after environment variables change.

    Returns:
        The application :class:`Settings`.
    """
    return Settings()  # type: ignore[call-arg]


def clear_settings_cache() -> None:
    """Clear the cached :class:`Settings` instance.

    Useful in tests or when environment variables change at runtime.
    """
    get_settings.cache_clear()
