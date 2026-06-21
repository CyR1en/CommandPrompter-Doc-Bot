"""Tests for :mod:`core.llm_client`.

The :class:`OpencodeClient` is replaced with a
:class:`FakeOpencodeClient` (or an :class:`AsyncMock`) so the tests
exercise :meth:`LLMClient.get_answer` without a real ``opencode serve``
server or HTTP transport.
"""

from __future__ import annotations

import json
from unittest.mock import AsyncMock, MagicMock

import httpx
import pytest

from core.llm_client import LLMClient
from core.opencode_client import OpencodeClientError


def _make_ndjson(*events: dict[str, object]) -> str:
    """Build NDJSON text from a sequence of event dicts."""
    return "\n".join(json.dumps(e) for e in events)


def _make_fake_client(
    *, return_text: str = "answer", raise_error: Exception | None = None
) -> MagicMock:
    """Build a fake :class:`OpencodeClient` for testing.

    Args:
        return_text: The text ``prompt`` returns when ``raise_error``
            is ``None``.
        raise_error: Optional exception to raise from ``prompt``.

    Returns:
        A :class:`MagicMock` whose ``prompt`` is an
        :class:`AsyncMock` returning ``return_text`` or raising
        ``raise_error``.
    """
    client = MagicMock(name="opencode_client")
    if raise_error is not None:
        client.prompt = AsyncMock(side_effect=raise_error)
    else:
        client.prompt = AsyncMock(return_value=return_text)
    client.create_session = AsyncMock(return_value="ses_0")
    client.delete_session = AsyncMock(return_value=None)
    client.close = AsyncMock(return_value=None)
    return client


# ---------------------------------------------------------------------------
# get_answer — happy path
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_get_answer_returns_concatenated_text() -> None:
    """The text returned by ``client.prompt`` is returned verbatim.

    (The NDJSON parsing lives in :mod:`core.opencode_client`; the LLM
    client just forwards the parsed string.)
    """
    client = _make_fake_client(return_text="Hello world!")
    llm = LLMClient(client=client, provider_id="opencode", model_id="m")

    answer = await llm.get_answer("q", session_id="ses_1")

    assert answer == "Hello world!"


@pytest.mark.asyncio
async def test_get_answer_returns_empty_string_when_no_text_events() -> None:
    """An empty-string prompt result is returned as ``""``."""
    client = _make_fake_client(return_text="")
    llm = LLMClient(client=client)

    answer = await llm.get_answer("q", session_id="ses_1")

    assert answer == ""


@pytest.mark.asyncio
async def test_get_answer_appends_length_constraint_to_query() -> None:
    """The query is augmented with the <2000-char constraint before sending."""
    client = _make_fake_client()
    llm = LLMClient(client=client)

    await llm.get_answer("how do I set spawn?", session_id="ses_1")

    client.prompt.assert_awaited_once()
    kwargs = client.prompt.await_args.kwargs
    parts = kwargs["parts"]
    assert len(parts) == 1
    text: str = parts[0]["text"]  # type: ignore[index]
    assert "how do I set spawn?" in text
    assert "Keep your answer less than 2000 characters." in text


# ---------------------------------------------------------------------------
# get_answer — session_id handling
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_get_answer_returns_none_when_session_id_missing() -> None:
    """A ``None`` session_id is a programming error → return ``None`` + warn."""
    client = _make_fake_client()
    llm = LLMClient(client=client)

    answer = await llm.get_answer("q", session_id=None)

    assert answer is None
    client.prompt.assert_not_called()


@pytest.mark.asyncio
async def test_get_answer_passes_session_id() -> None:
    """The ``session_id`` is forwarded to ``client.prompt``."""
    client = _make_fake_client()
    llm = LLMClient(client=client)

    await llm.get_answer("q", session_id="ses_42")

    kwargs = client.prompt.await_args.kwargs
    assert kwargs["session_id"] == "ses_42"


# ---------------------------------------------------------------------------
# get_answer — error handling
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_get_answer_returns_none_on_client_error(
    caplog: pytest.LogCaptureFixture,
) -> None:
    """An :class:`OpencodeClientError` → ``None`` + diagnostic log."""
    import logging

    client = _make_fake_client(
        raise_error=OpencodeClientError("prompt failed: 500")
    )
    llm = LLMClient(
        client=client,
        agent="docbot",
        provider_id="opencode",
        model_id="deepseek-v4-flash-free",
        variant="max",
    )

    caplog.set_level(logging.ERROR, logger="core.llm_client")
    answer = await llm.get_answer("q", session_id="ses_1")

    assert answer is None
    messages = "\n".join(r.message for r in caplog.records)
    assert "opencode prompt failed" in messages
    assert "agent=docbot" in messages
    assert "provider=opencode" in messages
    assert "model=deepseek-v4-flash-free" in messages
    assert "variant=max" in messages
    assert "session=ses_1" in messages


@pytest.mark.asyncio
async def test_get_answer_returns_none_on_http_error(
    caplog: pytest.LogCaptureFixture,
) -> None:
    """An :class:`httpx.HTTPError` → ``None`` + diagnostic log."""
    import logging

    client = _make_fake_client(
        raise_error=httpx.ConnectError("connection refused")
    )
    llm = LLMClient(
        client=client,
        provider_id="opencode",
        model_id="m",
    )

    caplog.set_level(logging.ERROR, logger="core.llm_client")
    answer = await llm.get_answer("q", session_id="ses_1")

    assert answer is None
    assert any(
        "opencode prompt failed" in r.message for r in caplog.records
    )


# ---------------------------------------------------------------------------
# get_answer — provider/model/variant overrides
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_get_answer_uses_instance_defaults() -> None:
    """Instance-level provider/model/variant are forwarded to ``prompt``."""
    client = _make_fake_client()
    llm = LLMClient(
        client=client,
        agent="docbot",
        provider_id="opencode",
        model_id="deepseek-v4-flash-free",
        variant="max",
    )

    await llm.get_answer("q", session_id="ses_1")

    kwargs = client.prompt.await_args.kwargs
    assert kwargs["agent"] == "docbot"
    assert kwargs["provider_id"] == "opencode"
    assert kwargs["model_id"] == "deepseek-v4-flash-free"
    assert kwargs["variant"] == "max"


@pytest.mark.asyncio
async def test_get_answer_per_call_override_wins() -> None:
    """Per-call kwargs override the instance defaults."""
    client = _make_fake_client()
    llm = LLMClient(
        client=client,
        provider_id="opencode",
        model_id="deepseek-v4-flash-free",
        variant="max",
    )

    await llm.get_answer(
        "q",
        session_id="ses_1",
        provider_id="anthropic",
        model_id="claude-sonnet-4-5",
        variant="low",
    )

    kwargs = client.prompt.await_args.kwargs
    assert kwargs["provider_id"] == "anthropic"
    assert kwargs["model_id"] == "claude-sonnet-4-5"
    assert kwargs["variant"] == "low"


@pytest.mark.asyncio
async def test_get_answer_variant_none_omitted_from_prompt_kwargs() -> None:
    """When variant is ``None`` at both levels, ``variant`` is ``None``.

    The LLM client always passes ``variant=<effective>`` to
    ``client.prompt``; when the effective value is ``None`` the
    :class:`OpencodeClient` omits the key from the HTTP body (tested
    in :mod:`tests.test_opencode_client`). Here we just assert the
    LLM client forwards ``None``.
    """
    client = _make_fake_client()
    llm = LLMClient(
        client=client,
        provider_id="opencode",
        model_id="m",
        variant=None,
    )

    await llm.get_answer("q", session_id="ses_1")

    kwargs = client.prompt.await_args.kwargs
    assert kwargs["variant"] is None


# ---------------------------------------------------------------------------
# Constructor
# ---------------------------------------------------------------------------


def test_constructor_stores_attributes() -> None:
    """The injected client and defaults are stored as attributes."""
    client = _make_fake_client()
    llm = LLMClient(
        client=client,
        agent="my-agent",
        provider_id="anthropic",
        model_id="claude-sonnet-4-5",
        variant="max",
    )

    assert llm.client is client
    assert llm.agent == "my-agent"
    assert llm.provider_id == "anthropic"
    assert llm.model_id == "claude-sonnet-4-5"
    assert llm.variant == "max"


def test_constructor_defaults() -> None:
    """Defaults are ``docbot`` and ``None`` for provider/model/variant."""
    client = _make_fake_client()
    llm = LLMClient(client=client)

    assert llm.agent == "docbot"
    assert llm.provider_id is None
    assert llm.model_id is None
    assert llm.variant is None
