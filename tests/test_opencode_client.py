"""Tests for :mod:`core.opencode_client`.

The HTTP transport is mocked with :class:`httpx.MockTransport` so the
tests exercise :class:`OpencodeClient` without a real ``opencode serve``
server. Each test builds a fresh client wired to a ``MockTransport``
handler and asserts on the request and the parsed response.
"""

from __future__ import annotations

import json
from typing import Any

import httpx
import pytest

from core.opencode_client import (
    OpencodeClient,
    OpencodeClientError,
    _extract_text_events,
)


def _make_client(
    handler: Any,
    *,
    password: str = "pw",
    timeout: float = 10.0,
) -> OpencodeClient:
    """Build an :class:`OpencodeClient` wired to a mock transport.

    Args:
        handler: A callable that takes an :class:`httpx.Request` and
            returns an :class:`httpx.Response` (or raises). Suitable for
            use as the ``app`` of :class:`httpx.MockTransport`.
        password: HTTP Basic Auth password. Defaults to ``"pw"``.
        timeout: Request timeout. Defaults to ``10.0`` (shorter than the
            production default so timeout tests run fast).

    Returns:
        An :class:`OpencodeClient` whose underlying
        :class:`httpx.AsyncClient` uses a :class:`httpx.MockTransport`.
    """
    transport = httpx.MockTransport(handler)
    client = OpencodeClient(
        base_url="http://127.0.0.1:4096",
        password=password,
        timeout=timeout,
    )
    # Swap in the mock transport so no real network calls happen.
    client._client = httpx.AsyncClient(
        base_url="http://127.0.0.1:4096",
        auth=("opencode", password),
        timeout=timeout,
        transport=transport,
    )
    return client


def _make_message_response(*parts: dict[str, Any]) -> str:
    """Build a ``POST /session/:id/message`` response body.

    The endpoint returns a single JSON object of the form
    ``{"info": Message, "parts": [Part, ...]}`` (per the opencode
    server's response schema). This helper builds the minimum viable
    body the bot cares about: just a ``parts`` list of the parts the
    test wants to assert on. ``info`` is omitted since the bot does
    not read it.

    Args:
        *parts: Part dicts, e.g. ``{"type": "text", "text": "..."}``.

    Returns:
        The JSON-encoded response body.
    """
    return json.dumps({"parts": list(parts)})


def _make_ndjson_body(*events: dict[str, Any]) -> str:
    """Build an NDJSON body from a sequence of event dicts.

    Kept for the NDJSON-fallback path of ``_extract_text_events`` and
    the corresponding legacy test. The current opencode server
    responses do not use this shape; the helper exists so the parser
    fallback can be exercised.

    Args:
        *events: Event dictionaries to encode, one per line.

    Returns:
        The JSON lines joined by newlines.
    """
    lines: list[str] = [json.dumps(event) for event in events]
    return "\n".join(lines)


# ---------------------------------------------------------------------------
# create_session
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_create_session_sends_correct_body_and_returns_id() -> None:
    """``create_session`` POSTs the right JSON and returns the session id.

    Regression guard: the model's id field is ``id`` (not ``modelID``)
    and the body has no ``metadata`` field — both are required by the
    opencode server's ``CreateInput`` schema
    (``packages/opencode/src/session/session.ts``).
    """
    captured: dict[str, Any] = {}

    def handler(request: httpx.Request) -> httpx.Response:
        captured["url"] = str(request.url)
        captured["method"] = request.method
        captured["body"] = json.loads(request.content.decode())
        captured["auth"] = request.headers.get("authorization")
        return httpx.Response(200, json={"id": "ses_01JABC"})

    client = _make_client(handler)
    try:
        sid = await client.create_session(
            title="discord:42",
            agent="docbot",
            provider_id="opencode",
            model_id="deepseek-v4-flash-free",
        )
    finally:
        await client.close()

    assert sid == "ses_01JABC"
    assert captured["method"] == "POST"
    assert captured["url"].endswith("/session")
    assert captured["body"]["title"] == "discord:42"
    assert captured["body"]["agent"] == "docbot"
    assert captured["body"]["model"] == {
        "id": "deepseek-v4-flash-free",
        "providerID": "opencode",
    }
    # The opencode server's CreateInput schema has no ``metadata`` field
    # — sending one was the cause of the ``400 Bad Request`` in the
    # original bot logs.
    assert "metadata" not in captured["body"]
    # The schema uses ``id`` for the model id, not ``modelID``.
    assert "modelID" not in captured["body"]["model"]


@pytest.mark.asyncio
async def test_create_session_does_not_send_metadata_field() -> None:
    """Regression guard: the body never contains a ``metadata`` key.

    The bot used to pass ``{"discordUserId": ...}`` as metadata; the
    opencode server rejected the request with 400. This test pins the
    fix.
    """
    captured: dict[str, Any] = {}

    def handler(request: httpx.Request) -> httpx.Response:
        captured["body"] = json.loads(request.content.decode())
        return httpx.Response(200, json={"id": "ses_x"})

    client = _make_client(handler)
    try:
        await client.create_session(
            title="t",
            agent="docbot",
            provider_id="opencode",
            model_id="m",
        )
    finally:
        await client.close()

    assert "metadata" not in captured["body"]


@pytest.mark.asyncio
async def test_create_session_raises_on_http_error() -> None:
    """A 5xx response raises :class:`OpencodeClientError`."""
    def handler(request: httpx.Request) -> httpx.Response:
        return httpx.Response(500, text="internal error")

    client = _make_client(handler)
    try:
        with pytest.raises(OpencodeClientError):
            await client.create_session(
                title="t",
                agent="docbot",
                provider_id="opencode",
                model_id="m",
            )
    finally:
        await client.close()


# ---------------------------------------------------------------------------
# prompt
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_prompt_sends_correct_body_and_returns_concatenated_text() -> None:
    """``prompt`` POSTs the right JSON and returns concatenated ``text`` parts.

    The message body uses ``model: {providerID, modelID}`` (different
    from the session create body, which uses ``model: {id, providerID}``).
    The response is a single JSON ``{info, parts}`` object, not NDJSON.
    """
    captured: dict[str, Any] = {}

    def handler(request: httpx.Request) -> httpx.Response:
        captured["url"] = str(request.url)
        captured["method"] = request.method
        captured["body"] = json.loads(request.content.decode())
        body = _make_message_response(
            {"type": "text", "text": "Hello "},
            {"type": "reasoning", "text": "..."},
            {"type": "text", "text": "world!"},
        )
        return httpx.Response(200, text=body)

    client = _make_client(handler)
    try:
        text = await client.prompt(
            session_id="ses_01J",
            parts=[{"type": "text", "text": "hi"}],
            agent="docbot",
            provider_id="opencode",
            model_id="deepseek-v4-flash-free",
        )
    finally:
        await client.close()

    assert text == "Hello world!"
    assert captured["method"] == "POST"
    assert "/session/ses_01J/message" in captured["url"]
    assert captured["body"]["parts"] == [{"type": "text", "text": "hi"}]
    assert captured["body"]["agent"] == "docbot"
    assert captured["body"]["model"] == {
        "providerID": "opencode",
        "modelID": "deepseek-v4-flash-free",
    }
    # The message body schema does not accept a ``format`` field — it
    # was a 400-causing field on the previous implementation.
    assert "format" not in captured["body"]


@pytest.mark.asyncio
async def test_prompt_includes_variant_when_set() -> None:
    """The ``variant`` key is included in the body when not ``None``."""
    captured: dict[str, Any] = {}

    def handler(request: httpx.Request) -> httpx.Response:
        captured["body"] = json.loads(request.content.decode())
        return httpx.Response(200, text="")

    client = _make_client(handler)
    try:
        await client.prompt(
            session_id="ses_1",
            parts=[{"type": "text", "text": "q"}],
            agent="docbot",
            provider_id="opencode",
            model_id="m",
            variant="max",
        )
    finally:
        await client.close()

    assert captured["body"]["variant"] == "max"


@pytest.mark.asyncio
async def test_prompt_omits_variant_when_none() -> None:
    """The ``variant`` key is absent from the body when ``None``."""
    captured: dict[str, Any] = {}

    def handler(request: httpx.Request) -> httpx.Response:
        captured["body"] = json.loads(request.content.decode())
        return httpx.Response(200, text="")

    client = _make_client(handler)
    try:
        await client.prompt(
            session_id="ses_1",
            parts=[{"type": "text", "text": "q"}],
            agent="docbot",
            provider_id="opencode",
            model_id="m",
        )
    finally:
        await client.close()

    assert "variant" not in captured["body"]


@pytest.mark.asyncio
async def test_prompt_parses_single_json_response() -> None:
    """The ``{info, parts}`` response: text parts are concatenated in order."""
    def handler(request: httpx.Request) -> httpx.Response:
        body = _make_message_response(
            {"type": "text", "text": "A"},
            {"type": "text", "text": "B"},
            {"type": "text", "text": "C"},
        )
        return httpx.Response(200, text=body)

    client = _make_client(handler)
    try:
        text = await client.prompt(
            session_id="ses_1",
            parts=[{"type": "text", "text": "q"}],
            agent="docbot",
            provider_id="opencode",
            model_id="m",
        )
    finally:
        await client.close()

    assert text == "ABC"


@pytest.mark.asyncio
async def test_prompt_skips_non_text_parts() -> None:
    """Reasoning/tool parts are ignored; only ``text`` parts contribute."""
    def handler(request: httpx.Request) -> httpx.Response:
        body = _make_message_response(
            {"type": "tool", "name": "read"},
            {"type": "text", "text": "only this"},
            {"type": "reasoning", "text": "thinking..."},
        )
        return httpx.Response(200, text=body)

    client = _make_client(handler)
    try:
        text = await client.prompt(
            session_id="ses_1",
            parts=[{"type": "text", "text": "q"}],
            agent="docbot",
            provider_id="opencode",
            model_id="m",
        )
    finally:
        await client.close()

    assert text == "only this"


@pytest.mark.asyncio
async def test_prompt_returns_empty_string_when_no_text_parts() -> None:
    """An HTTP 200 with no ``text`` parts returns ``""``."""
    def handler(request: httpx.Request) -> httpx.Response:
        body = _make_message_response(
            {"type": "tool", "name": "read"},
            {"type": "reasoning", "text": "..."},
        )
        return httpx.Response(200, text=body)

    client = _make_client(handler)
    try:
        text = await client.prompt(
            session_id="ses_1",
            parts=[{"type": "text", "text": "q"}],
            agent="docbot",
            provider_id="opencode",
            model_id="m",
        )
    finally:
        await client.close()

    assert text == ""


@pytest.mark.asyncio
async def test_prompt_raises_on_http_error() -> None:
    """A 5xx response raises :class:`OpencodeClientError`."""
    def handler(request: httpx.Request) -> httpx.Response:
        return httpx.Response(500, text="boom")

    client = _make_client(handler)
    try:
        with pytest.raises(OpencodeClientError):
            await client.prompt(
                session_id="ses_1",
                parts=[{"type": "text", "text": "q"}],
                agent="docbot",
                provider_id="opencode",
                model_id="m",
            )
    finally:
        await client.close()


@pytest.mark.asyncio
async def test_prompt_raises_on_timeout() -> None:
    """A request timeout surfaces as :class:`OpencodeClientError`."""
    def handler(request: httpx.Request) -> httpx.Response:
        raise httpx.TimeoutException("timed out")

    client = _make_client(handler, timeout=0.1)
    try:
        with pytest.raises(OpencodeClientError):
            await client.prompt(
                session_id="ses_1",
                parts=[{"type": "text", "text": "q"}],
                agent="docbot",
                provider_id="opencode",
                model_id="m",
            )
    finally:
        await client.close()


# ---------------------------------------------------------------------------
# delete_session
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_delete_session_uses_delete_method() -> None:
    """``delete_session`` sends ``DELETE /session/:id``."""
    captured: dict[str, Any] = {}

    def handler(request: httpx.Request) -> httpx.Response:
        captured["method"] = request.method
        captured["url"] = str(request.url)
        return httpx.Response(204)

    client = _make_client(handler)
    try:
        await client.delete_session("ses_01J")
    finally:
        await client.close()

    assert captured["method"] == "DELETE"
    assert "/session/ses_01J" in captured["url"]


@pytest.mark.asyncio
async def test_delete_session_ignores_404() -> None:
    """A 404 response is silently ignored (session already gone)."""
    def handler(request: httpx.Request) -> httpx.Response:
        return httpx.Response(404, text="not found")

    client = _make_client(handler)
    try:
        # Should not raise.
        await client.delete_session("ses_gone")
    finally:
        await client.close()


@pytest.mark.asyncio
async def test_delete_session_raises_on_500() -> None:
    """A 5xx response (other than 404) raises :class:`OpencodeClientError`."""
    def handler(request: httpx.Request) -> httpx.Response:
        return httpx.Response(500, text="boom")

    client = _make_client(handler)
    try:
        with pytest.raises(OpencodeClientError):
            await client.delete_session("ses_1")
    finally:
        await client.close()


# ---------------------------------------------------------------------------
# Auth header
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_basic_auth_header_set() -> None:
    """Every request carries an ``Authorization: Basic ...`` header."""
    import base64

    captured: dict[str, str] = {}

    def handler(request: httpx.Request) -> httpx.Response:
        captured["auth"] = request.headers.get("authorization", "")
        return httpx.Response(200, json={"id": "ses_x"})

    client = _make_client(handler, password="hunter2")
    try:
        await client.create_session(
            title="t",
            agent="docbot",
            provider_id="opencode",
            model_id="m",
        )
    finally:
        await client.close()

    expected = "Basic " + base64.b64encode(b"opencode:hunter2").decode()
    assert captured["auth"] == expected


# ---------------------------------------------------------------------------
# _extract_text_events helper
# ---------------------------------------------------------------------------


def test_extract_text_events_concatenates_text_events() -> None:
    """``_extract_text_events`` concatenates ``text`` events from NDJSON."""
    body = _make_ndjson_body(
        {"type": "text", "part": {"text": "A"}},
        {"type": "tool", "name": "bash"},
        {"type": "text", "part": {"text": "B"}},
    )
    assert _extract_text_events(body) == "AB"


def test_extract_text_events_returns_empty_on_no_text() -> None:
    """No ``text`` events yields an empty string."""
    body = _make_ndjson_body({"type": "done"})
    assert _extract_text_events(body) == ""


def test_extract_text_events_returns_empty_on_empty_string() -> None:
    """An empty body yields an empty string."""
    assert _extract_text_events("") == ""


def test_extract_text_events_skips_malformed_lines() -> None:
    """Malformed JSON lines are skipped without raising."""
    body = (
        '{"type":"text","part":{"text":"ok"}}\n'
        "GARBAGE\n"
        '{"type":"text","part":{"text":"!"}}'
    )
    assert _extract_text_events(body) == "ok!"


def test_extract_text_events_handles_sse_format() -> None:
    """Handles Server-Sent Events (SSE) format returned by newer opencode versions."""
    body = (
        "event: message\n"
        'data: {"type": "text", "text": "Hello "}\n'
        "\n"
        "event: message\n"
        'data: {"type": "reasoning", "text": "thinking..."}\n'
        "\n"
        "event: message\n"
        'data: {"type": "text", "text": "world!"}\n'
    )
    assert _extract_text_events(body) == "Hello world!"


# ---------------------------------------------------------------------------
# Async context manager protocol
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_async_context_manager_closes_client() -> None:
    """The ``async with`` protocol closes the client on exit."""
    def handler(request: httpx.Request) -> httpx.Response:
        return httpx.Response(200, json={"id": "ses_x"})

    transport = httpx.MockTransport(handler)
    async with OpencodeClient(
        base_url="http://127.0.0.1:4096", password="pw"
    ) as client:
        client._client = httpx.AsyncClient(
            base_url="http://127.0.0.1:4096",
            auth=("opencode", "pw"),
            transport=transport,
        )
        sid = await client.create_session(
            title="t",
            agent="docbot",
            provider_id="opencode",
            model_id="m",
        )
        assert sid == "ses_x"
