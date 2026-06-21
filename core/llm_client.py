"""OpenCode-backed LLM client for the CMDP Doc Bot.

This module provides :class:`LLMClient`, a thin async wrapper around
:class:`core.opencode_client.OpencodeClient` that turns a user's
question into a concatenated ``text`` answer from the docbot agent.

Why HTTP instead of ``opencode run`` per query
----------------------------------------------

The previous architecture spawned ``opencode run`` per query. Two
problems forced the migration to a long-lived ``opencode serve`` server
+ HTTP API:

1. ``opencode run --session <id>`` is **continue-only** — it
   hard-fails with "Session not found" if the session does not already
   exist, and there is no public ``opencode session create`` subcommand
   nor any way to specify a custom session ID. So per-user session
   continuity was not achievable via the CLI alone.

2. Two simultaneous ``opencode run --session <id>`` calls race: the
   in-process ``Session.assertNotBusy`` lock does not span subprocesses
   (issue #11699). The server owns the in-process state authoritatively,
   so the HTTP API is the only race-free way to get per-user session
   continuity.

This class therefore holds an injected :class:`OpencodeClient` and
forwards each prompt to a specific session id (managed by
:class:`core.session_manager.SessionManager`). The NDJSON parsing of
the streaming response lives in :func:`core.opencode_client._extract_text_events`.
"""

from __future__ import annotations

import httpx

from core.logger import get_logger
from core.opencode_client import OpencodeClient, OpencodeClientError

_logger = get_logger(__name__)


class LLMClient:
    """Async client that answers queries via the opencode HTTP API.

    The client is configured with an injected :class:`OpencodeClient`
    (which talks to the long-lived ``opencode serve`` subprocess), the
    agent name (``"docbot"``), and the default provider/model/variant
    triple (typically wired from ``LLM_PROVIDER``/``LLM_MODEL``/
    ``LLM_VARIANT`` in :mod:`main`). Each call to :meth:`get_answer`
    sends the query to a specific session id and parses the streaming
    NDJSON response into concatenated ``text`` events.

    Per-call ``provider_id``/``model_id``/``variant`` kwargs override
    the instance defaults; ``None`` at both levels means the instance
    default is used. The session id is always required at the bot
    layer (a ``None`` session id is a programming error and is handled
    defensively by returning ``None`` with a warning log).

    Attributes:
        client: The injected :class:`OpencodeClient` used to talk to
            the opencode server.
        agent: Name of the OpenCode agent entry to invoke. Defaults to
            ``"docbot"``.
        provider_id: Default provider ID (e.g. ``"opencode"``).
        model_id: Default bare model id (e.g.
            ``"deepseek-v4-flash-free"``).
        variant: Default reasoning-effort variant (e.g. ``"max"``).
            ``None`` lets the model use its default.
    """

    def __init__(
        self,
        *,
        client: OpencodeClient,
        agent: str = "docbot",
        provider_id: str | None = None,
        model_id: str | None = None,
        variant: str | None = None,
    ) -> None:
        """Initialize the LLM client.

        Args:
            client: The injected :class:`OpencodeClient` used to talk
                to the opencode server.
            agent: Name of the OpenCode agent entry to invoke. Defaults
                to ``"docbot"``.
            provider_id: Default provider ID (e.g. ``"opencode"``).
                ``None`` means the caller must pass a per-call
                override.
            model_id: Default bare model id (e.g.
                ``"deepseek-v4-flash-free"``). ``None`` means the
                caller must pass a per-call override.
            variant: Default reasoning-effort variant (e.g. ``"max"``).
                ``None`` lets the model use its default.
        """
        self.client: OpencodeClient = client
        self.agent: str = agent
        self.provider_id: str | None = provider_id
        self.model_id: str | None = model_id
        self.variant: str | None = variant

    async def get_answer(
        self,
        query: str,
        *,
        session_id: str | None = None,
        provider_id: str | None = None,
        model_id: str | None = None,
        variant: str | None = None,
    ) -> str | None:
        """Answer a user question via the opencode HTTP API.

        Sends the query to ``session_id`` via
        :meth:`OpencodeClient.prompt` and parses the streaming NDJSON
        response into concatenated ``text`` events. Per-call
        ``provider_id``/``model_id``/``variant`` kwargs override the
        instance defaults; ``None`` at both levels means the instance
        default is used.

        Args:
            query: The user's natural-language question.
            session_id: The opencode session id to prompt. The bot
                layer always passes one (from
                :class:`core.session_manager.SessionManager`); a
                ``None`` value is a programming error and is handled
                defensively by returning ``None`` with a warning log.
            provider_id: Optional provider ID override. When ``None``
                (the default) the instance default ``self.provider_id``
                is used.
            model_id: Optional model id override. When ``None`` (the
                default) the instance default ``self.model_id`` is
                used.
            variant: Optional variant override. When ``None`` (the
                default) the instance default ``self.variant`` is used.

        Returns:
            The concatenated ``text`` content from the agent's response
            events. Returns an empty string when the HTTP call
            succeeded but the agent emitted no ``text`` events.
            Returns ``None`` when the HTTP call failed
            (:class:`OpencodeClientError` or :class:`httpx.HTTPError`)
            — diagnostic information is logged at ``ERROR`` level (the
            session id, the model/variant that were requested) so the
            caller can surface a fallback message without trying to
            render a half-formed answer.
        """
        if session_id is None:
            _logger.warning(
                "get_answer called without a session_id; returning None "
                "(the bot layer should always pass one from "
                "SessionManager.get_or_create)"
            )
            return None

        # Per-call override wins; fall back to the instance default.
        effective_provider: str | None = (
            provider_id if provider_id is not None else self.provider_id
        )
        effective_model: str | None = (
            model_id if model_id is not None else self.model_id
        )
        effective_variant: str | None = (
            variant if variant is not None else self.variant
        )

        prompt_query: str = (
            query + "\n\n Keep your answer less than 2000 characters."
        )
        _logger.debug(
            "Prompting opencode (agent=%s, provider=%s, model=%s, "
            "variant=%s, session=%s, query=%d chars)",
            self.agent,
            effective_provider,
            effective_model,
            effective_variant,
            session_id,
            len(prompt_query),
        )

        try:
            answer: str = await self.client.prompt(
                session_id=session_id,
                parts=[{"type": "text", "text": prompt_query}],
                agent=self.agent,
                provider_id=effective_provider or "",
                model_id=effective_model or "",
                variant=effective_variant,
            )
        except (OpencodeClientError, httpx.HTTPError) as exc:
            # HTTP failure (5xx, timeout, connection error). Log enough
            # context to diagnose from the bot logs alone: the session
            # id, the model/variant that were requested, and the
            # exception. Returning ``None`` lets the caller distinguish
            # a real HTTP failure from a successful call that simply
            # produced no text events, so it can surface a fallback
            # message instead of trying to render ``""``.
            _logger.error(
                "opencode prompt failed (agent=%s, provider=%s, "
                "model=%s, variant=%s, session=%s): %s",
                self.agent,
                effective_provider,
                effective_model,
                effective_variant,
                session_id,
                exc,
            )
            return None

        _logger.debug(
            "opencode returned %d character(s) of text", len(answer)
        )
        return answer
