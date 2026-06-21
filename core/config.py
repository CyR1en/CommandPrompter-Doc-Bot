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

from pydantic import field_validator, model_validator
from pydantic_settings import BaseSettings, NoDecode, SettingsConfigDict


class Settings(BaseSettings):
    """Runtime configuration for the CMDP Doc Bot.

    Settings are populated from environment variables (or a local
    ``.env`` file). The required fields must be provided before the bot
    can fully start.

    The bot targets OpenCode's built-in LLM providers (loaded at runtime
    from https://models.dev/api.json) by default: set ``LLM_PROVIDER`` to
    the provider ID known to OpenCode (``opencode`` for OpenCode Zen,
    ``anthropic``, ``openai``, ``google``, ...) and ``LLM_API_KEY`` to
    the provider's API key. At startup :mod:`main` maps ``LLM_API_KEY``
    to the env var the provider's SDK expects (e.g.
    ``OPENCODE_API_KEY`` for the ``opencode`` provider,
    ``ANTHROPIC_API_KEY`` for ``anthropic``) so the built-in provider
    picks the key up automatically — no custom provider block is written
    to ``opencode.json``.

    The legacy ``custom-llm`` shim remains as an escape hatch: set
    ``LLM_PROVIDER=custom-llm`` together with ``LLM_BASE_URL`` and the
    bot will write a custom OpenAI-compatible provider block into
    ``opencode.json`` instead of relying on the built-in catalog.

    Attributes:
        DISCORD_TOKEN: Authentication token for the Discord bot account.
        LLM_PROVIDER: Provider ID known to OpenCode / Models.dev.
            Defaults to ``"opencode"`` (OpenCode Zen,
            https://opencode.ai/docs/zen/). Set to ``"custom-llm"`` to
            use the legacy OpenAI-compatible shim (requires
            ``LLM_BASE_URL``).
        LLM_API_KEY: API key used to authenticate with the LLM provider.
            Mapped to the provider's declared env var at startup (e.g.
            ``OPENCODE_API_KEY``, ``ANTHROPIC_API_KEY``).
        LLM_BASE_URL: Base URL of a custom OpenAI-compatible LLM
            endpoint. Only required when
            ``LLM_PROVIDER == "custom-llm"``; ignored for built-in
            providers. Defaults to ``None``.
        LLM_MODEL: The bare model name (without provider prefix) to use
            for LLM chat generation, e.g. ``deepseek-v4-flash-free``.
        LLM_VARIANT: Optional reasoning-effort variant (model-specific),
            e.g. ``"max"``, ``"high"``, ``"low"``. ``None`` lets the
            model use its default. List valid variants for a model with
            ``opencode models --verbose``.
        GITHUB_TOKEN: Optional GitHub token used to authenticate when
            cloning/pulling private repositories.
        REPO_URLS: Git repository URLs to clone and compile for the
            full-context documentation. May be supplied as a JSON-encoded
            list or a comma-separated string.
        POLL_INTERVAL_MINUTES: Interval, in minutes, between repository
            polling for upstream changes.
        OPENCODE_SERVER_HOST: Hostname the long-lived
            ``opencode serve`` subprocess binds to. Defaults to
            ``"127.0.0.1"`` (loopback only — the bot and the server
            run on the same host).
        OPENCODE_SERVER_PORT: TCP port the ``opencode serve`` subprocess
            binds to. Defaults to ``4096`` (the opencode default).
        OPENCODE_SERVER_PASSWORD: HTTP Basic Auth password for the
            ``opencode serve`` subprocess. When ``None`` (the default)
            a secure random password is generated at startup via
            :func:`secrets.token_urlsafe(32)` so each bot run uses a
            fresh password. Set explicitly to keep the same password
            across restarts (useful for attaching via ``opencode web``
            for debugging).
        SESSION_TTL_MINUTES: Per-user opencode session idle TTL in
            minutes. After this idle period, the user's session is
            deleted on the server and a fresh one is created on the
            next message. Defaults to ``30``.
    """

    model_config = SettingsConfigDict(
        env_file=".env",
        env_file_encoding="utf-8",
        case_sensitive=True,
        extra="ignore",
    )

    DISCORD_TOKEN: str
    LLM_PROVIDER: str = "opencode"
    LLM_API_KEY: str
    LLM_BASE_URL: str | None = None
    LLM_MODEL: str = "deepseek-v4-flash-free"
    LLM_VARIANT: str | None = None
    GITHUB_TOKEN: str | None = None
    # ``NoDecode`` tells pydantic-settings to pass the raw environment value
    # through to the ``_split_repo_urls`` validator instead of trying to
    # JSON-decode it first, which lets us accept a comma-separated string.
    REPO_URLS: Annotated[list[str], NoDecode]
    POLL_INTERVAL_MINUTES: int = 10

    # --- opencode server (long-lived subprocess started by the bot) ---
    OPENCODE_SERVER_HOST: str = "127.0.0.1"
    OPENCODE_SERVER_PORT: int = 4096
    OPENCODE_SERVER_PASSWORD: str | None = None  # auto-generate if unset

    # --- session manager ---
    SESSION_TTL_MINUTES: int = 30

    @model_validator(mode="after")
    def _require_base_url_for_custom_shim(self) -> "Settings":
        """Require ``LLM_BASE_URL`` when using the ``custom-llm`` shim.

        Built-in providers (``opencode``, ``anthropic``, ``openai`` ...)
        resolve their endpoint from OpenCode's built-in catalog (loaded
        from https://models.dev/api.json) and do not need a base URL.
        The legacy ``custom-llm`` shim writes a custom provider block
        into ``opencode.json`` and requires ``LLM_BASE_URL`` to point at
        the OpenAI-compatible endpoint.

        Returns:
            The validated :class:`Settings` instance.

        Raises:
            ValueError: If ``LLM_PROVIDER == "custom-llm"`` and
                ``LLM_BASE_URL`` is not set. Pydantic surfaces this as a
                :class:`pydantic.ValidationError`.
        """
        if self.LLM_PROVIDER == "custom-llm" and not self.LLM_BASE_URL:
            raise ValueError(
                "LLM_BASE_URL is required when LLM_PROVIDER == 'custom-llm' "
                "(the custom OpenAI-compatible shim needs an endpoint URL)."
            )
        return self

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
