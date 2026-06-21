"""Tests for :mod:`main`.

Two test groups:

1. ``_publish_provider_env_var`` — publishes ``settings.LLM_API_KEY`` to
   the env var the active LLM provider's SDK expects (e.g.
   ``OPENCODE_API_KEY`` for the ``opencode`` provider). The mapping is a
   16-entry table in :data:`main._PROVIDER_ENV_VAR`; the tests below pin
   the table values that have historically been easy to get wrong
   (notably the ``vercel`` provider, whose env var name is
   ``AI_GATEWAY_API_KEY`` rather than the obvious ``VERCEL_API_KEY``).

2. ``build_session_sweeper_task`` — the background sweeper that deletes
   expired opencode sessions. The test verifies the sweeper calls
   ``delete_session`` for each expired entry and ``remove`` s it from
   the session manager.
"""

from __future__ import annotations

import logging
import os
from unittest.mock import AsyncMock, MagicMock, patch

import pytest

from main import _PROVIDER_ENV_VAR, _publish_provider_env_var
from main import build_session_sweeper_task


def _make_settings(
    provider: str, api_key: str = "test-key"
) -> MagicMock:
    """Build a fake :class:`core.config.Settings` with the given provider.

    Only the attributes read by :func:`_publish_provider_env_var` are
    populated; the rest of the mock auto-generates attribute access.

    Args:
        provider: Value for ``LLM_PROVIDER``.
        api_key: Value for ``LLM_API_KEY``.

    Returns:
        A :class:`MagicMock` with the two attributes set.
    """
    settings = MagicMock(name="settings")
    settings.LLM_PROVIDER = provider
    settings.LLM_API_KEY = api_key
    return settings


def _make_logger() -> MagicMock:
    """Build a fake :class:`logging.Logger` that records ``.warning`` calls."""
    return MagicMock(spec=logging.Logger)


@pytest.mark.parametrize(
    "provider, expected_env_var",
    [
        ("opencode", "OPENCODE_API_KEY"),
        ("anthropic", "ANTHROPIC_API_KEY"),
        ("openai", "OPENAI_API_KEY"),
        # Regression guard: vercel's env var is ``AI_GATEWAY_API_KEY``
        # (it uses the @ai-sdk/gateway SDK), NOT ``VERCEL_API_KEY``.
        ("vercel", "AI_GATEWAY_API_KEY"),
    ],
)
def test_publishes_known_provider_env_var(
    monkeypatch: pytest.MonkeyPatch,
    provider: str,
    expected_env_var: str,
) -> None:
    """A known provider's env var is set from ``LLM_API_KEY``."""
    monkeypatch.delenv(expected_env_var, raising=False)
    settings = _make_settings(provider)
    logger = _make_logger()

    _publish_provider_env_var(settings, logger)

    assert os.environ[expected_env_var] == "test-key"
    logger.warning.assert_not_called()


def test_skips_custom_llm_sentinel(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """The ``custom-llm`` sentinel: no env var is set, no warning is logged.

    The custom-shim's key is written into ``opencode.json`` by
    :func:`core.opencode_config.setup_opencode`, not into the
    environment, so the helper must be a no-op for that provider.
    """
    # Snapshot the env so we can assert nothing relevant changed.
    snapshot = dict(os.environ)
    settings = _make_settings("custom-llm")
    logger = _make_logger()

    _publish_provider_env_var(settings, logger)

    assert os.environ == snapshot
    logger.warning.assert_not_called()


def test_warns_on_unknown_provider(
    caplog: pytest.LogCaptureFixture,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """An unknown provider logs a warning and sets no env var."""
    snapshot = dict(os.environ)
    settings = _make_settings("github-copilot")
    logger = _make_logger()

    with caplog.at_level(logging.WARNING, logger="test_main"):
        _publish_provider_env_var(settings, logger)

    assert os.environ == snapshot
    logger.warning.assert_called_once()
    # ``call_args.args[0]`` is the format string; ``call_args.args[1]``
    # is the provider name passed via %r formatting. Assert the format
    # string mentions the map and the provider name is the unknown one.
    format_str = logger.warning.call_args.args[0]
    provider_name = logger.warning.call_args.args[1]
    assert "not in the built-in env-var map" in format_str
    assert provider_name == "github-copilot"


def test_does_not_overwrite_existing_env_var(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """An env var already set in the environment is left alone.

    This guards the ``os.environ.setdefault`` behaviour: explicit env vars
    (e.g. a CI secret already present) win over ``LLM_API_KEY`` so the
    bot cannot accidentally downgrade credentials.
    """
    monkeypatch.setenv("ANTHROPIC_API_KEY", "explicit-shell-key")
    settings = _make_settings("anthropic", api_key="llm-api-key-from-env-file")
    logger = _make_logger()

    _publish_provider_env_var(settings, logger)

    assert os.environ["ANTHROPIC_API_KEY"] == "explicit-shell-key"


def test_provider_env_var_table_contains_expected_providers() -> None:
    """The mapping includes the providers documented in the README.

    This is a structural guard so a future refactor that accidentally
    drops a provider from the table breaks the test suite rather than
    silently breaking auth for users of that provider.
    """
    expected: set[str] = {
        "opencode",
        "anthropic",
        "openai",
        "google",
        "groq",
        "mistral",
        "deepseek",
        "openrouter",
        "xai",
        "cohere",
        "vercel",
        "azure",
        "perplexity",
        "deepinfra",
        "togetherai",
        "cerebras",
        "custom-llm",
    }
    assert set(_PROVIDER_ENV_VAR.keys()) == expected


# ---------------------------------------------------------------------------
# build_session_sweeper_task
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_sweeper_deletes_expired_sessions() -> None:
    """The sweeper deletes each expired session on the server."""
    session_manager = MagicMock(name="session_manager")
    session_manager.cleanup_expired = MagicMock(
        return_value=[(42, "ses_a"), (99, "ses_b")]
    )
    session_manager.remove = MagicMock(return_value=None)
    client = MagicMock(name="client")
    client.delete_session = AsyncMock(return_value=None)

    sweeper = build_session_sweeper_task(
        session_manager=session_manager, client=client, interval_minutes=1
    )

    # Invoke the sweeper's body once via the underlying coroutine
    # function (``.coro`` is the function; calling it returns the
    # coroutine).
    await sweeper.coro()

    assert client.delete_session.await_count == 2
    deleted = [c.args[0] for c in client.delete_session.await_args_list]
    assert deleted == ["ses_a", "ses_b"]
    assert session_manager.remove.call_count == 2
    session_manager.cleanup_expired.assert_called_once()


@pytest.mark.asyncio
async def test_sweeper_skips_when_nothing_expired() -> None:
    """The sweeper is a no-op when ``cleanup_expired`` returns ``[]``."""
    session_manager = MagicMock(name="session_manager")
    session_manager.cleanup_expired = MagicMock(return_value=[])
    session_manager.remove = MagicMock(return_value=None)
    client = MagicMock(name="client")
    client.delete_session = AsyncMock(return_value=None)

    sweeper = build_session_sweeper_task(
        session_manager=session_manager, client=client, interval_minutes=1
    )
    await sweeper.coro()

    client.delete_session.assert_not_awaited()
    session_manager.remove.assert_not_called()


@pytest.mark.asyncio
async def test_sweeper_continues_on_delete_failure() -> None:
    """A failed ``delete_session`` does not abort the sweep.

    The sweeper logs a warning and still removes the entry from the
    session manager so the mapping does not keep pointing at a session
    that may or may not be gone.
    """
    session_manager = MagicMock(name="session_manager")
    session_manager.cleanup_expired = MagicMock(
        return_value=[(1, "ses_a"), (2, "ses_b")]
    )
    session_manager.remove = MagicMock(return_value=None)
    client = MagicMock(name="client")
    client.delete_session = AsyncMock(
        side_effect=[RuntimeError("boom"), None]
    )

    sweeper = build_session_sweeper_task(
        session_manager=session_manager, client=client, interval_minutes=1
    )
    # Suppress the warning log so the test output stays clean.
    with patch("main.get_logger") as mock_logger:
        mock_logger.return_value.warning = MagicMock()
        await sweeper.coro()

    assert client.delete_session.await_count == 2
    # Both entries are removed even though the first delete failed.
    assert session_manager.remove.call_count == 2


# ---------------------------------------------------------------------------
# _run_bot lifecycle (server start/stop)
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_run_bot_starts_and_stops_server(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """``_run_bot`` starts the opencode server and stops it on shutdown.

    The ``bot.start`` call is mocked to raise a custom ``_Shutdown``
    exception so the ``finally`` block runs and we can assert
    ``server.stop`` and ``bot.close`` were called. (``KeyboardInterrupt``
    is not used because pytest treats it as an abort signal.)
    """
    from main import _run_bot

    class _Shutdown(Exception):
        """Sentinel raised by the fake ``bot.run`` to stop the loop."""

    # Build a minimal fake Settings with all attributes _run_bot reads.
    settings = MagicMock(name="settings")
    settings.LLM_PROVIDER = "opencode"
    settings.LLM_API_KEY = "key"
    settings.LLM_BASE_URL = None
    settings.LLM_MODEL = "deepseek-v4-flash-free"
    settings.LLM_VARIANT = None
    settings.OPENCODE_SERVER_HOST = "127.0.0.1"
    settings.OPENCODE_SERVER_PORT = 4096
    settings.OPENCODE_SERVER_PASSWORD = None
    settings.SESSION_TTL_MINUTES = 30
    settings.REPO_URLS = []
    settings.POLL_INTERVAL_MINUTES = 10
    settings.GITHUB_TOKEN = None
    settings.DISCORD_TOKEN = "token"

    logger = MagicMock(spec=logging.Logger)

    # Mock all the external dependencies so no real subprocess / Discord
    # / filesystem operations happen.
    mock_server = MagicMock(name="server")
    mock_server.is_running = True
    mock_server.start = AsyncMock()
    mock_server.stop = AsyncMock()
    mock_server.base_url = "http://127.0.0.1:4096"
    mock_server.password = "pw"
    mock_client = MagicMock(name="client")
    mock_client.close = AsyncMock()

    with patch("main.setup_opencode") as mock_setup, \
         patch("main.OpencodeServer", return_value=mock_server), \
         patch("main.OpencodeClient", return_value=mock_client), \
         patch("main.SessionManager") as mock_sm_cls, \
         patch("main.RepositoryManager") as mock_rm_cls, \
         patch("main.RateLimiter") as mock_rl_cls, \
         patch("main.LLMClient") as mock_llm_cls, \
         patch("main.DocBot") as mock_bot_cls, \
         patch("main.build_polling_task") as mock_poll, \
         patch("main.build_session_sweeper_task") as mock_sweep:
        # ``build_polling_task`` / ``build_session_sweeper_task`` return
        # a MagicMock with ``is_running`` and ``cancel``.
        mock_task = MagicMock()
        mock_task.is_running.return_value = True
        mock_task.cancel = MagicMock()
        mock_poll.return_value = mock_task
        mock_sweep.return_value = MagicMock(
            is_running=MagicMock(return_value=True),
            cancel=MagicMock(),
        )
        # ``DocBot`` instance: ``setup_hook`` is assignable and ``start``
        # raises ``_Shutdown`` to simulate the bot being stopped. The
        # ``finally`` block also calls ``bot.close`` so we mock it.
        mock_bot = MagicMock(name="bot")
        mock_bot.is_closed.return_value = False
        mock_bot.close = AsyncMock()

        async def raise_shutdown(*_a, **_k):
            raise _Shutdown()

        mock_bot.start = raise_shutdown
        mock_bot_cls.return_value = mock_bot

        with pytest.raises(_Shutdown):
            await _run_bot(settings, logger)

    mock_server.start.assert_awaited_once()
    mock_server.stop.assert_awaited_once()
    mock_client.close.assert_awaited_once()
    mock_bot.close.assert_awaited_once()

