"""Async HTTP client for the ``opencode serve`` API.

This module provides :class:`OpencodeClient`, a thin async wrapper
around :class:`httpx.AsyncClient` that talks to the long-lived
``opencode serve`` subprocess started by
:class:`core.opencode_server.OpencodeServer`. It exposes the three
endpoints the bot needs:

* :meth:`create_session` — ``POST /session``: create a fresh session and
  return its auto-generated id.
* :meth:`prompt` — ``POST /session/:id/message``: send a prompt to a
  session and parse the streaming NDJSON response into concatenated
  ``text`` events.
* :meth:`delete_session` — ``DELETE /session/:id``: best-effort cleanup
  (ignores 404).

Auth is HTTP Basic Auth with username ``opencode`` and the password
agreed with the server (see
:class:`core.opencode_server.OpencodeServer`).

Why HTTP instead of ``opencode run`` per query
----------------------------------------------

``opencode run --session <id>`` is continue-only (it hard-fails if the
session does not exist) and two simultaneous calls on the same session
race because the in-process ``Session.assertNotBusy`` lock does not span
subprocesses (issue #11699). The server owns the in-process state
authoritatively, so the HTTP API is the only way to get per-user
session continuity without the race.
"""

from __future__ import annotations

import json
from typing import Any

import httpx

from core.logger import get_logger

_logger = get_logger(__name__)


class OpencodeClientError(RuntimeError):
    """Raised when an opencode server HTTP call fails.

    Wraps the underlying :class:`httpx.HTTPError` so the caller
    (:class:`core.llm_client.LLMClient`) can catch a single exception
    type and surface a fallback message. ``httpx.TimeoutException`` is
    a subclass of ``httpx.HTTPError`` so timeouts are covered too.
    """


def _extract_text_events(body: str) -> str:
    """Concatenate ``text`` parts from an opencode HTTP response body.

    The ``POST /session/:id/message`` endpoint returns a single JSON
    object of the form ``{"info": Message, "parts": [Part, ...]}`` where
    each part with ``type == "text"`` carries the assistant's text in a
    ``text`` field. This helper extracts those ``text`` fields in order
    and concatenates them.

    As a safety net, falls back to the legacy NDJSON shape
    ``{"type": "text", "part": {"text": "..."}}`` (the ``opencode run
    --format json`` output format) when the body is not a single JSON
    object with a ``parts`` field — this keeps the helper working if
    some endpoint or future opencode version returns a streaming
    response.

    Args:
        body: The raw response body (UTF-8 decoded).

    Returns:
        The concatenated assistant text. Returns an empty string when
        the body has no ``text`` parts/events.
    """
    stripped: str = body.strip()
    if not stripped:
        return ""

    # Primary path: single-JSON ``{"info": ..., "parts": [...]}``.
    try:
        data: Any = json.loads(stripped)
        if isinstance(data, dict) and isinstance(data.get("parts"), list):
            texts: list[str] = []
            for part in data["parts"]:
                if (
                    isinstance(part, dict)
                    and part.get("type") == "text"
                    and isinstance(part.get("text"), str)
                ):
                    texts.append(part["text"])
            return "".join(texts)
    except json.JSONDecodeError:
        pass

    # Fallback: NDJSON ``{"type": "text", "part": {"text": "..."}}``.
    parts: list[str] = []
    for line in body.splitlines():
        line = line.strip()
        if not line:
            continue
        try:
            event: dict[str, Any] = json.loads(line)
        except json.JSONDecodeError:
            _logger.debug("Skipping non-JSON opencode line: %s", line)
            continue
        if event.get("type") == "text":
            part_obj: object = event.get("part", {})
            if isinstance(part_obj, dict):
                text: object = part_obj.get("text", "")
                if isinstance(text, str):
                    parts.append(text)
    return "".join(parts)


class OpencodeClient:
    """Async HTTP client for the opencode server API.

    Wraps a single :class:`httpx.AsyncClient` configured with HTTP Basic
    Auth (username ``opencode``, password agreed with the server). The
    client is cheap to construct but holds an underlying connection
    pool, so callers should reuse one instance for the bot's lifetime
    and call :meth:`close` on shutdown (or use the async context
    manager protocol).

    Attributes:
        base_url: The server base URL (e.g.
            ``"http://127.0.0.1:4096"``).
        username: HTTP Basic Auth username (always ``"opencode"``).
        timeout: Request timeout in seconds. Defaults to ``120.0``
            because LLM calls can be slow.
    """

    def __init__(
        self,
        *,
        base_url: str,
        username: str = "opencode",
        password: str,
        timeout: float = 120.0,
    ) -> None:
        """Initialize the HTTP client.

        Args:
            base_url: The server base URL (e.g.
                ``"http://127.0.0.1:4096"``).
            username: HTTP Basic Auth username. Defaults to
                ``"opencode"`` (the only username the server accepts).
            password: HTTP Basic Auth password. Must match the
                ``OPENCODE_SERVER_PASSWORD`` the server was started
                with.
            timeout: Request timeout in seconds. Defaults to ``120.0``
                because LLM calls can be slow.
        """
        self.base_url: str = base_url
        self.username: str = username
        self.timeout: float = timeout
        self._client: httpx.AsyncClient = httpx.AsyncClient(
            base_url=base_url,
            auth=(username, password),
            timeout=timeout,
        )

    # ------------------------------------------------------------------
    # Session lifecycle
    # ------------------------------------------------------------------

    async def create_session(
        self,
        *,
        title: str,
        provider_id: str,
        model_id: str,
        agent: str | None = None,
    ) -> str:
        """Create a new opencode session and return its id.

        Sends ``POST /session`` with the title, optional agent, and
        model (``{id, providerID}``). The server generates and returns
        a ``ses_<26-char ULID>`` id.

        Note:
            The ``CreateInput`` schema on the opencode server
            (``packages/opencode/src/session/session.ts``) only accepts
            these fields plus optional ``parentID``, ``permission``,
            and ``workspaceID``. There is **no** ``metadata`` field —
            the previous implementation passed one and the server
            rejected the request with ``400 Bad Request``. There is
            also no ``modelID`` field on the model; the schema's
            ``Model`` struct uses ``id`` for the model id.

        Args:
            title: Human-readable session title (e.g.
                ``"discord:<user_id>"``).
            provider_id: Provider ID (e.g. ``"opencode"``,
                ``"anthropic"``).
            model_id: Bare model id (e.g. ``"deepseek-v4-flash-free"``).
            agent: Optional agent entry to use (e.g. ``"docbot"``).
                ``None`` (the default) omits the field so the opencode
                server uses its built-in default agent. The bot's
                :class:`core.llm_client.LLMClient` always passes
                ``"docbot"``; the AGENT.md-generation helper in
                :mod:`bot.tasks` passes ``None``.

        Returns:
            The auto-generated session id (e.g.
            ``"ses_01J..."``).

        Raises:
            OpencodeClientError: If the HTTP call fails (5xx, timeout,
                connection error, or 4xx with a body the client cannot
                parse).
        """
        body: dict[str, Any] = {
            "title": title,
            "model": {"id": model_id, "providerID": provider_id},
        }
        if agent is not None:
            body["agent"] = agent
        try:
            resp = await self._client.post("/session", json=body)
            resp.raise_for_status()
        except httpx.HTTPError as exc:
            raise OpencodeClientError(
                f"create_session failed: {exc}"
            ) from exc
        data: Any = resp.json()
        if not isinstance(data, dict) or "id" not in data:
            raise OpencodeClientError(
                f"create_session returned unexpected body: {data!r}"
            )
        session_id: Any = data["id"]
        if not isinstance(session_id, str):
            raise OpencodeClientError(
                f"create_session returned non-string id: {session_id!r}"
            )
        _logger.debug(
            "Created opencode session (id=%s, title=%s, agent=%s, "
            "provider=%s, model=%s)",
            session_id,
            title,
            agent,
            provider_id,
            model_id,
        )
        return session_id

    async def prompt(
        self,
        *,
        session_id: str,
        parts: list[dict[str, object]],
        provider_id: str,
        model_id: str,
        agent: str | None = None,
        variant: str | None = None,
    ) -> str:
        """Send a prompt to a session and return the concatenated text.

        Sends ``POST /session/:id/message`` with the message parts,
        optional agent, model, and optional variant. Reads the full
        response body (a single JSON ``{info, parts}`` object) and
        concatenates the ``text`` parts.

        Args:
            session_id: The session id returned by :meth:`create_session`.
            parts: Message parts, e.g.
                ``[{"type": "text", "text": "the user's question"}]``.
            provider_id: Provider ID (e.g. ``"opencode"``).
            model_id: Bare model id (e.g. ``"deepseek-v4-flash-free"``).
            agent: Optional agent entry to use (e.g. ``"docbot"``).
                ``None`` (the default) omits the field so the opencode
                server uses its built-in default agent. The bot's
                :class:`core.llm_client.LLMClient` always passes
                ``"docbot"``; the AGENT.md-generation helper in
                :mod:`bot.tasks` passes ``None``.
            variant: Optional reasoning-effort variant (e.g.
                ``"max"``). ``None`` lets the model use its default.

        Returns:
            The concatenated ``text`` content from the response
            ``parts``. Returns an empty string when the server returned
            HTTP 200 but the agent emitted no ``text`` parts.

        Raises:
            OpencodeClientError: If the HTTP call fails (4xx/5xx,
                timeout, connection error). The caller distinguishes
                this from the empty-string case.
        """
        body: dict[str, Any] = {
            "parts": parts,
            "model": {"providerID": provider_id, "modelID": model_id},
        }
        if agent is not None:
            body["agent"] = agent
        if variant is not None:
            body["variant"] = variant
        url: str = f"/session/{session_id}/message"
        try:
            resp = await self._client.post(url, json=body)
            resp.raise_for_status()
        except httpx.HTTPError as exc:
            raise OpencodeClientError(
                f"prompt failed (session={session_id}): {exc}"
            ) from exc
        text_body: str = resp.text
        return _extract_text_events(text_body)

    async def delete_session(self, session_id: str) -> None:
        """Delete a session; ignore 404 (already gone).

        Best-effort cleanup: the session storage at
        ``~/.local/share/opencode/storage/`` is the server's
        authoritative state, so deleting a session that no longer
        exists (e.g. the server was restarted) is a no-op.

        Args:
            session_id: The session id to delete.

        Raises:
            OpencodeClientError: If the HTTP call fails with anything
                other than 404.
        """
        url: str = f"/session/{session_id}"
        try:
            resp = await self._client.delete(url)
            if resp.status_code == 404:
                _logger.debug(
                    "delete_session: %s already gone (404)", session_id
                )
                return
            resp.raise_for_status()
        except httpx.HTTPError as exc:
            # httpx.HTTPStatusError is a subclass of HTTPError; a 404
            # that slipped past the explicit check above would land
            # here, so we re-check the status code on the exception.
            status = getattr(exc, "response", None)
            if status is not None and getattr(status, "status_code", None) == 404:
                _logger.debug(
                    "delete_session: %s already gone (404 via exc)",
                    session_id,
                )
                return
            raise OpencodeClientError(
                f"delete_session failed (session={session_id}): {exc}"
            ) from exc

    # ------------------------------------------------------------------
    # Resource cleanup
    # ------------------------------------------------------------------

    async def close(self) -> None:
        """Close the underlying :class:`httpx.AsyncClient` connection pool."""
        await self._client.aclose()

    async def __aenter__(self) -> "OpencodeClient":
        """Enter the async context manager protocol."""
        return self

    async def __aexit__(self, *exc: object) -> None:
        """Close the client on context exit."""
        await self.close()
