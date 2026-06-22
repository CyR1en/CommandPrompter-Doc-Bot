"""Discord client wiring for the CMDP Doc Bot.

This module provides :class:`DocBot`, a thin :class:`discord.Client`
subclass that turns incoming @mentions into answers produced by an
OpenCode-backed LLM client. A :class:`~core.rate_limiter.RateLimiter`
gates per-user questions, a
:class:`~core.session_manager.SessionManager` maps each Discord user to
a long-lived opencode session (so the agent can reference prior context
within a 30-minute idle window), and an
:class:`~core.llm_client.LLMClient` produces the answers by prompting
the session via the opencode HTTP API. All three are injected through
the constructor.

Per-user session flow
---------------------

On each @mention:

1. The rate limiter is consulted (sessions do not bypass rate limits).
2. :meth:`SessionManager.get_or_create` returns the user's active
   session id (creating one if there is no entry or the existing one
   has been idle past the TTL).
3. The per-user :class:`asyncio.Lock` is acquired so the same user's
   prompts serialize (different users run in parallel).
4. :meth:`LLMClient.get_answer` prompts the session and parses the
   streaming NDJSON response.
5. On a non-empty answer, :meth:`SessionManager.touch` refreshes the
   session's ``last_active`` so the TTL clock keeps rolling for an
   active conversation.

The persona and repository context are handled entirely by OpenCode:
the ``docbot`` agent definition lives in
``~/.config/opencode/agents/docbot.md`` (written at startup by
:func:`core.opencode_config.setup_opencode`), and the agent reads the
cloned repositories directly from the server's working directory
(typically ``data/repos``) using its tools.
"""

from __future__ import annotations

import re
from typing import Any

import discord

from core.llm_client import LLMClient
from core.logger import get_logger
from core.message_splitter import split_message
from core.rate_limiter import RateLimiter
from core.session_manager import SessionManager

_logger = get_logger(__name__)


def _default_intents() -> discord.Intents:
    """Return the intents the bot needs to operate.

    Starts from :func:`discord.Intents.default` (which enables message
    receive events) and additionally enables the privileged
    ``message_content`` intent, which is required to read the content of
    messages so the bot can extract the user's question.

    Returns:
        A configured :class:`discord.Intents` instance.
    """
    intents = discord.Intents.default()
    intents.message_content = True
    return intents


class DocBot(discord.Client):
    """Discord client that answers @mention questions via the LLM client.

    The bot ignores messages from other bots and only responds when it is
    explicitly @mentioned. Each user is rate-limited through the injected
    :class:`RateLimiter`; when over quota a polite retry message is sent
    instead of invoking the LLM. Allowed queries are mapped to a
    per-user opencode session via the injected
    :class:`SessionManager`, serialized behind a per-user lock, and
    forwarded to :meth:`LLMClient.get_answer` while a typing indicator
    is shown to the user. The answer is sent back as a reply.

    Attributes:
        rate_limiter: Sliding-window limiter used to gate per-user
            queries.
        llm_client: LLM client used to answer user questions.
        session_manager: Per-user opencode session manager.
        provider_id: Default provider ID forwarded to
            :meth:`SessionManager.get_or_create` and
            :meth:`LLMClient.get_answer` (e.g. ``"opencode"``).
        model_id: Default bare model id forwarded to the session
            manager and LLM client (e.g.
            ``"deepseek-v4-flash-free"``).
        variant: Default reasoning-effort variant forwarded to the LLM
            client (e.g. ``"max"``). ``None`` lets the model use its
            default.
    """

    def __init__(
        self,
        rate_limiter: RateLimiter,
        llm_client: LLMClient,
        session_manager: SessionManager,
        *,
        provider_id: str | None = None,
        model_id: str | None = None,
        variant: str | None = None,
        intents: discord.Intents | None = None,
        **options: Any,
    ) -> None:
        """Initialize the bot client.

        Args:
            rate_limiter: Per-user rate limiter consulted on every
                inbound mention.
            llm_client: LLM client used to answer user questions.
            session_manager: Per-user opencode session manager.
            provider_id: Default provider ID (e.g. ``"opencode"``).
                Forwarded to :meth:`SessionManager.get_or_create` and
                :meth:`LLMClient.get_answer` on every prompt.
            model_id: Default bare model id (e.g.
                ``"deepseek-v4-flash-free"``). Forwarded to the session
                manager and LLM client on every prompt.
            variant: Default reasoning-effort variant (e.g. ``"max"``).
                ``None`` lets the model use its default.
            intents: Discord intents. Defaults to
                :func:`discord.Intents.default` with ``message_content``
                enabled, which is required to read message content. Pass
                a custom value to narrow or broaden events.
            **options: Additional keyword options forwarded to
                :class:`discord.Client` (e.g. ``max_messages``).
        """
        super().__init__(intents=intents or _default_intents(), **options)
        self.rate_limiter: RateLimiter = rate_limiter
        self.llm_client: LLMClient = llm_client
        self.session_manager: SessionManager = session_manager
        self.provider_id: str | None = provider_id
        self.model_id: str | None = model_id
        self.variant: str | None = variant

    async def on_ready(self) -> None:
        """Log a startup banner once the client has connected.

        Emits the bot's tag and id once the gateway identifies the
        client. When ``self.user`` is not yet populated (e.g. the event
        fires before identification completes) a fallback line is logged
        instead of raising.
        """
        if self.user is not None:
            _logger.info(
                "DocBot connected as %s (id=%s)",
                self.user,
                self.user.id,
            )
        else:
            _logger.info("DocBot connected (user unavailable)")

    async def on_message(self, message: discord.Message) -> None:
        """Handle an inbound message.

        The handler enforces the bot's interaction contract:

        1. Messages from bots (including this bot itself) are ignored.
        2. The bot only responds when it or one of its roles is explicitly
           @mentioned.
        3. The per-user rate limit is checked; over-quota users receive a
           polite retry message and the LLM client is *not* invoked.
        4. For allowed queries the bot's user or role mention is stripped
           from the content, the user's opencode session is looked up
           (or created), a typing indicator is shown, and the remaining
           text is passed to :meth:`LLMClient.get_answer` under the
           per-user lock. The answer is sent back as a reply chain (the
           first chunk replies to ``message``; longer answers that exceed
           Discord's 2000-character per-message limit are split by
           :func:`core.message_splitter.split_message` and the rest are
           posted as replies to the previous bot message). On a non-empty
           answer the session's ``last_active`` is refreshed so the TTL
           clock keeps rolling.

        Args:
            message: The inbound Discord message.
        """
        _logger.info(
            "Received message from %s: %s (mentions: %s)",
            message.author,
            message.content,
            [u.name for u in message.mentions],
        )
        # (1) Ignore other bots and our own messages.
        if message.author.bot:
            return

        # (2) Only respond to explicit @mentions of this bot or its roles.
        if self.user is None:
            return

        is_mentioned: bool = self.user in message.mentions
        if not is_mentioned and message.guild is not None:
            me: discord.Member | None = message.guild.me
            if me is not None:
                is_mentioned = any(
                    role in me.roles for role in message.role_mentions
                )

        if not is_mentioned:
            return

        user_id: int = message.author.id

        # (3) Enforce the per-user sliding-window rate limit.
        if not self.rate_limiter.is_allowed(user_id):
            retry_after: float = self.rate_limiter.get_retry_after(user_id)
            _logger.info(
                "Rate limit hit for user %s; retry in %.1fs",
                user_id,
                retry_after,
            )
            await message.reply(
                "You're asking questions a bit too quickly. "
                f"Please try again in {int(retry_after)} second(s)."
            )
            return

        # (4) Strip the mention, answer the remaining query.
        query: str = self._extract_query(message)
        if not query:
            return

        # Look up (or create) the user's opencode session and prompt it
        # under the per-user lock so the entire
        # get-or-create → prompt critical section is serialized per
        # user. Without this, two concurrent @mentions from the same
        # user could both pass the existence check in
        # ``get_or_create`` and each create a separate session on the
        # server (orphaning one of them). Done inside the typing
        # indicator so the user sees "bot is typing..." while the
        # session is being created and the LLM call runs.
        async with message.channel.typing():
            async with self.session_manager.lock_for(user_id):
                session_id: str = await self.session_manager.get_or_create(
                    user_id,
                    title=f"discord:{user_id}",
                    agent=self.llm_client.agent,
                    provider_id=self.provider_id,
                    model_id=self.model_id,
                )
                answer: str | None = await self.llm_client.get_answer(
                    query,
                    session_id=session_id,
                    provider_id=self.provider_id,
                    model_id=self.model_id,
                    variant=self.variant,
                )

        # Touch on success (keeps the TTL clock rolling for an active
        # conversation). Done outside the typing indicator and the lock
        # so a slow touch does not block other users.
        if answer:
            self.session_manager.touch(user_id)

        # ``get_answer`` returns ``None`` when the HTTP call failed
        # (with diagnostic info already logged) and ``""`` when the
        # call succeeded but the agent emitted no text events. Either
        # case means the user asked a question and got no answer back
        # — send a polite fallback instead of letting Discord reject
        # the reply as an empty message.
        if not answer:
            _logger.warning(
                "LLM returned no answer for user %s (query=%d chars)",
                user_id,
                len(query),
            )
            await message.reply(
                "Sorry, I couldn't generate an answer for that. "
                "Please try again or rephrase your question."
            )
            return

        await self._send_chunked(message.channel, answer, reference=message)

    async def _send_chunked(
        self,
        channel: discord.abc.Messageable,
        text: str,
        *,
        reference: discord.Message,
    ) -> None:
        """Send ``text`` to ``channel`` as a chain of Discord-safe replies.

        Discord rejects messages longer than 2000 characters with HTTP
        400. This helper splits the text with
        :func:`core.message_splitter.split_message` and posts each chunk
        sequentially: the first chunk is a reply to ``reference`` (the
        user's @mention message) and each subsequent chunk is a reply to
        the previous bot message, so Discord's UI chains them visually
        and the user sees a single threaded response.

        Args:
            channel: The Discord channel/textable to send the chunks to.
            text: The text to send. May exceed Discord's per-message
                limit; the helper handles splitting.
            reference: The user's original @mention message. The first
                chunk is posted as a reply to this message.
        """
        chunks: list[str] = split_message(text)
        # First chunk: reply to the user's message so the response is
        # threaded under their question.
        previous: discord.Message = await reference.reply(chunks[0])
        # Subsequent chunks: reply to the previous bot message so
        # Discord chains them visually.
        for chunk in chunks[1:]:
            previous = await previous.reply(chunk)

    def _extract_query(self, message: discord.Message) -> str:
        """Strip the bot's mention tokens from ``message.content``.

        Discord renders a user mention as ``<@user_id>`` or, for
        nicknames, ``<@!user_id>``. Role mentions are rendered as
        ``<@&role_id>``. Both user and role mentions for the bot are
        removed so the remaining text is the user's actual question.

        Args:
            message: The message to extract the query from.

        Returns:
            The message content with the bot's user and role mentions
            removed and surrounding whitespace stripped. Returns an empty
            string if the bot user is not yet known or if nothing remains
            after stripping the mentions.
        """
        if self.user is None:
            return ""
        content: str = message.content or ""
        cleaned: str = re.sub(rf"<@!?{self.user.id}>", "", content)
        if message.guild is not None and message.guild.me is not None:
            for role in message.guild.me.roles:
                cleaned = cleaned.replace(f"<@&{role.id}>", "")
        return cleaned.strip()
