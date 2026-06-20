"""Discord client wiring for the CMDP Doc Bot.

This module provides :class:`DocBot`, a thin :class:`discord.Client`
subclass that turns incoming @mentions into answers produced by an
OpenCode-backed LLM client. A :class:`~core.rate_limiter.RateLimiter`
gates per-user questions and an :class:`~core.llm_client.LLMClient`
produces the answers by spawning an ``opencode run`` subprocess, both
injected through the constructor.

The persona and repository context are handled entirely by OpenCode:
the ``docbot`` agent definition lives in
``~/.config/opencode/agents/docbot.md`` (written at startup by
:func:`core.opencode_config.setup_opencode`), and the agent reads the
cloned repositories directly from ``data/repos`` using its tools. The
Discord client therefore only needs to extract the user's question and
forward it to :meth:`LLMClient.get_answer`.
"""

from __future__ import annotations

import re
from typing import Any

import discord

from core.llm_client import LLMClient
from core.logger import get_logger
from core.rate_limiter import RateLimiter

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
    instead of invoking the LLM. Allowed queries are forwarded to
    :meth:`LLMClient.get_answer` while a typing indicator is shown to
    the user, and the answer is sent back as a reply.

    Attributes:
        rate_limiter: Sliding-window limiter used to gate per-user
            queries.
        llm_client: LLM client used to answer user questions.
    """

    def __init__(
        self,
        rate_limiter: RateLimiter,
        llm_client: LLMClient,
        *,
        intents: discord.Intents | None = None,
        **options: Any,
    ) -> None:
        """Initialize the bot client.

        Args:
            rate_limiter: Per-user rate limiter consulted on every
                inbound mention.
            llm_client: LLM client used to answer user questions.
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
           from the content, a typing indicator is shown, and the remaining
           text is passed to :meth:`LLMClient.get_answer`. The answer is
           sent back as a reply to the message.

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

        # (4) Strip the mention, answer the remaining query under a
        # typing indicator.
        query: str = self._extract_query(message)
        if not query:
            return

        async with message.channel.typing():
            answer: str = await self.llm_client.get_answer(query)

        await message.reply(answer)

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
