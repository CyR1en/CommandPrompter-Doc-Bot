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
from datetime import datetime, timezone
from typing import Any

import discord

from core.llm_client import LLMClient
from core.logger import get_logger
from core.markdown_sanitizer import sanitize_markdown
from core.message_splitter import split_message
from core.rate_limiter import RateLimiter
from core.session_manager import SessionManager

_logger = get_logger(__name__)


#: Maximum characters per embed ``description`` field. Discord's hard
#: limit is 4096 — we use 3800 to leave a small safety margin for
#: future footer/timestamp tweaks and to avoid hitting the cap on
#: text the splitter was unable to shrink (e.g. a single code block
#: longer than the limit).
_EMBED_DESCRIPTION_LIMIT: int = 3800

#: Brand color for bot embeds. Discord's official blurple accent
#: (``#5865F2``) — fits a Discord bot and stays visually consistent
#: across all answers.
_EMBED_COLOR: discord.Colour = discord.Colour(0x5865F2)

#: Footer text used when an answer spans more than one embed page.
#: ``{current}`` is the 1-based page index, ``{total}`` the total
#: number of pages. Kept short so it does not eat into the
#: description's character budget.
_PAGE_FOOTER: str = "Page {current}/{total}"


def _build_answer_embeds(text: str) -> list[discord.Embed]:
    """Wrap an LLM answer into a chain of Discord embeds.

    Splits ``text`` into chunks that each fit inside an embed
    ``description`` field (:data:`_EMBED_DESCRIPTION_LIMIT` characters),
    then wraps each chunk in an embed with:

    * ``description`` — the chunk of the answer (carries the prose,
      including any code blocks; triple-backtick fences are preserved
      across chunk boundaries by :func:`core.message_splitter.split_message`).
    * ``color`` — the bot's brand color (:data:`_EMBED_COLOR`).
    * ``timestamp`` — UTC "now", so the embed footer shows when the
      answer was generated.
    * ``footer`` — ``"Page i/N"`` when the answer spans more than one
      page. Short answers (single page) do not get a footer so the
      response stays visually quiet.

    Args:
        text: The LLM-generated answer. May be empty (an empty
            description is allowed) or arbitrarily long (will be split
            into multiple embeds).

    Returns:
        A non-empty list of embeds in the same order as the chunks
        they wrap. ``split_message`` always returns at least one
        element, so this never returns an empty list.
    """
    chunks: list[str] = split_message(text, limit=_EMBED_DESCRIPTION_LIMIT)
    total: int = len(chunks)
    now: datetime = datetime.now(timezone.utc)
    embeds: list[discord.Embed] = []
    for index, chunk in enumerate(chunks, start=1):
        embed: discord.Embed = discord.Embed(
            description=chunk,
            color=_EMBED_COLOR,
            timestamp=now,
        )
        if total > 1:
            embed.set_footer(text=_PAGE_FOOTER.format(
                current=index, total=total,
            ))
        embeds.append(embed)
    return embeds


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
    is shown to the user. The answer is delivered as one or more
    :class:`discord.Embed` reply messages (built by
    :func:`_build_answer_embeds`), so the response renders as a
    Discord card with a consistent brand color, a generation
    timestamp, and — for long answers — a ``"Page i/N"`` footer.
    Oversized answers are split across multiple embeds with
    triple-backtick code-block fences preserved across pages.

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
           per-user lock. The answer is run through
           :func:`core.markdown_sanitizer.sanitize_markdown` to rewrite
           Discord-incompatible markdown (GFM tables, horizontal rules,
           H4+ headers) into Discord-friendly equivalents, then
           delivered as a chain of embeds via :meth:`_send_embeds`
           (the first embed replies to ``message``; answers that
           exceed the per-embed description budget are split by
           :func:`core.message_splitter.split_message` and the rest
           are posted as embeds replying to the previous bot message,
           with a ``"Page i/N"`` footer on every page). On a non-empty
           answer the session's ``last_active`` is refreshed so the
           TTL clock keeps rolling.

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
            # Rewrite Discord-incompatible markdown (GFM tables,
            # horizontal rules, H4+ headers) into Discord-compatible
            # equivalents before delivery. Defense-in-depth — the
            # agent persona already tells the model to avoid these,
            # but the sanitizer guarantees the user never sees
            # broken rendering.
            answer = sanitize_markdown(answer)

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

        await self._send_embeds(message.channel, answer, reference=message)

    async def _send_embeds(
        self,
        channel: discord.abc.Messageable,
        text: str,
        *,
        reference: discord.Message,
    ) -> None:
        """Send ``text`` to ``channel`` as a chain of reply embeds.

        Wraps the answer into one or more :class:`discord.Embed` objects
        via :func:`_build_answer_embeds` and posts them sequentially.
        The first embed is a reply to ``reference`` (the user's
        @mention message); each subsequent embed is a reply to the
        previous bot message, so Discord's UI chains them into a
        single threaded response. Long answers are paginated by the
        splitter (one embed per page, with a ``"Page i/N"`` footer).

        Args:
            channel: The Discord channel/textable the embeds are
                posted to. Currently unused (the chain is anchored on
                ``reference`` and ``previous`` messages) but kept in
                the signature for symmetry with the previous helper
                and for future use (e.g. cross-posting to a log
                channel).
            text: The LLM-generated answer. May be empty (yields a
                single empty-description embed) or arbitrarily long
                (will be split into multiple embeds).
            reference: The user's original @mention message. The
                first embed is posted as a reply to this message.
        """
        del channel  # currently unused; kept for future cross-posting
        embeds: list[discord.Embed] = _build_answer_embeds(text)
        # First embed: reply to the user's message so the response is
        # threaded under their question.
        previous: discord.Message = await reference.reply(embed=embeds[0])
        # Subsequent embeds: reply to the previous bot message so
        # Discord chains them visually.
        for embed in embeds[1:]:
            previous = await previous.reply(embed=embed)

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
