"""Tests for :mod:`bot.client`.

The Discord :class:`~discord.Message`, :class:`~discord.User`, and
:class:`~discord.TextChannel` interfaces are replaced with lightweight
fakes so the tests exercise :class:`DocBot.on_message` without a Discord
gateway connection. The :class:`~core.rate_limiter.RateLimiter` and
:class:`~core.llm_client.LLMClient` are mocked so the rate-limiting,
mention-checking, and LLM-invocation branches can be asserted
independently.

The :class:`DocBot` is constructed normally (its constructor does not
connect to Discord) and ``self.user`` is populated through the
``_connection.user`` backing attribute that the read-only ``Client.user``
property returns.
"""

from __future__ import annotations

from collections.abc import AsyncIterator
from contextlib import asynccontextmanager
from typing import Any
from unittest.mock import AsyncMock, MagicMock

import pytest

from bot.client import DocBot

#: The bot's own user id used throughout the test suite.
_BOT_USER_ID: int = 99


class _TypingTracker:
    """Count typing context manager entries and exits.

    Attributes:
        entered: Number of ``async with channel.typing()`` entries.
        exited: Number of corresponding exits.
    """

    def __init__(self) -> None:
        """Initialize the counters at zero."""
        self.entered: int = 0
        self.exited: int = 0

    @asynccontextmanager
    async def __call__(self) -> AsyncIterator[None]:
        """Yield once while recording an entry/exit pair."""
        self.entered += 1
        try:
            yield
        finally:
            self.exited += 1


def _make_channel(tracker: _TypingTracker) -> MagicMock:
    """Build a fake text channel wired to the typing tracker.

    Args:
        tracker: The :class:`_TypingTracker` to drive the typing
            context manager.

    Returns:
        A :class:`MagicMock` whose ``typing()`` returns an async context
        manager backed by ``tracker`` and whose ``send`` is an
        :class:`AsyncMock` returning a fake sent message.
    """
    channel = MagicMock(name="channel")
    channel.typing.side_effect = tracker
    channel.send = AsyncMock(return_value=MagicMock(name="sent_message"))
    return channel


def _make_message(
    *,
    author_id: int = 42,
    author_bot: bool = False,
    mentions: list[Any] | None = None,
    content: str = "",
    channel: MagicMock | None = None,
    tracker: _TypingTracker | None = None,
) -> tuple[MagicMock, MagicMock, _TypingTracker]:
    """Build a fake message with its channel and typing tracker.

    Args:
        author_id: The id of the message author.
        author_bot: Whether the author is a bot.
        mentions: The list of users mentioned in the message.
        content: The raw message content (may include a mention token).
        channel: An optional pre-built channel; one is created otherwise.
        tracker: An optional pre-built tracker; one is created otherwise.

    Returns:
        A ``(message, channel, tracker)`` triple.
    """
    author = MagicMock(name="author")
    author.id = author_id
    author.bot = author_bot

    if tracker is None:
        tracker = _TypingTracker()
    if channel is None:
        channel = _make_channel(tracker)

    message = MagicMock(name="message")
    message.author = author
    message.mentions = mentions if mentions is not None else []
    message.content = content
    message.channel = channel
    message.guild = None
    message.role_mentions = []
    message.reply = AsyncMock(return_value=MagicMock(name="sent_message"))
    return message, channel, tracker


def _make_bot_user(user_id: int = _BOT_USER_ID) -> MagicMock:
    """Build a fake bot user with the given id.

    Args:
        user_id: The id to assign to the bot user.

    Returns:
        A :class:`MagicMock` with ``id`` set to ``user_id``.
    """
    user = MagicMock(name="bot_user")
    user.id = user_id
    return user


@pytest.fixture()
def rate_limiter() -> MagicMock:
    """Return a mocked :class:`RateLimiter` that allows by default.

    Returns:
        A :class:`MagicMock` whose ``is_allowed`` returns ``True`` and
        ``get_retry_after`` returns ``0.0``. Individual tests override
        these as needed.
    """
    limiter = MagicMock(name="rate_limiter")
    limiter.is_allowed.return_value = True
    limiter.get_retry_after.return_value = 0.0
    return limiter


@pytest.fixture()
def llm_client() -> MagicMock:
    """Return a mocked :class:`LLMClient` with an async ``get_answer``.

    Returns:
        A :class:`MagicMock` whose ``get_answer`` is an
        :class:`AsyncMock` returning ``"answer-text"``.
    """
    client = MagicMock(name="llm_client")
    client.get_answer = AsyncMock(return_value="answer-text")
    return client


@pytest.fixture()
def bot(
    rate_limiter: MagicMock,
    llm_client: MagicMock,
) -> DocBot:
    """Return a :class:`DocBot` with mocked deps and a populated user.

    The bot is constructed normally (no gateway connection) and
    ``self.user`` is set via the ``_connection.user`` backing attribute
    so mention checks can succeed without logging in.

    Args:
        rate_limiter: The mocked rate limiter fixture.
        llm_client: The mocked LLM client fixture.

    Returns:
        A :class:`DocBot` whose ``self.user.id`` is :data:`_BOT_USER_ID`.
    """
    client = DocBot(rate_limiter, llm_client)
    client._connection.user = _make_bot_user()  # type: ignore[attr-defined]
    return client


@pytest.mark.asyncio
async def test_ignores_messages_from_bots(
    bot: DocBot, rate_limiter: MagicMock, llm_client: MagicMock
) -> None:
    """Messages authored by bots are dropped before any logic runs.

    The rate limiter and LLM client must not be consulted, and no reply is
    sent.
    """
    message, channel, tracker = _make_message(author_bot=True)
    await bot.on_message(message)

    rate_limiter.is_allowed.assert_not_called()
    llm_client.get_answer.assert_not_called()
    message.reply.assert_not_called()
    assert tracker.entered == 0


@pytest.mark.asyncio
async def test_ignores_messages_without_mention(
    bot: DocBot, rate_limiter: MagicMock, llm_client: MagicMock
) -> None:
    """Messages that do not @mention the bot are ignored.

    A message mentioning a *different* user must not trigger a response.
    """
    other_user = MagicMock(name="other_user")
    other_user.id = 1234
    message, channel, tracker = _make_message(
        mentions=[other_user], content="hello world"
    )
    await bot.on_message(message)

    rate_limiter.is_allowed.assert_not_called()
    llm_client.get_answer.assert_not_called()
    message.reply.assert_not_called()
    assert tracker.entered == 0


@pytest.mark.asyncio
async def test_ignores_message_when_bot_user_unknown(
    rate_limiter: MagicMock, llm_client: MagicMock
) -> None:
    """When ``self.user`` is ``None`` mentions are never matched.

    Guards the early return so a pre-``on_ready`` message cannot raise.
    """
    client = DocBot(rate_limiter, llm_client)
    # ``self.user`` stays None (no _connection.user set).
    message, channel, tracker = _make_message(
        mentions=[MagicMock()], content="hi"
    )
    await client.on_message(message)

    rate_limiter.is_allowed.assert_not_called()
    message.reply.assert_not_called()


@pytest.mark.asyncio
async def test_rate_limited_sends_rejection_and_skips_llm(
    bot: DocBot, rate_limiter: MagicMock, llm_client: MagicMock
) -> None:
    """An over-quota user gets a polite retry message and no LLM call.

    The rejection message must mention the remaining wait time and the
    typing indicator must not be shown (no LLM call is pending).
    """
    rate_limiter.is_allowed.return_value = False
    rate_limiter.get_retry_after.return_value = 42.7

    bot_user = bot.user
    assert bot_user is not None
    message, channel, tracker = _make_message(
        mentions=[bot_user], content=f"<@{_BOT_USER_ID}> anything"
    )
    await bot.on_message(message)

    rate_limiter.is_allowed.assert_called_once_with(42)
    rate_limiter.get_retry_after.assert_called_once_with(42)
    llm_client.get_answer.assert_not_called()
    assert tracker.entered == 0
    message.reply.assert_awaited_once()
    sent_text: str = message.reply.await_args.args[0]
    assert "42" in sent_text
    assert "second" in sent_text


@pytest.mark.asyncio
async def test_answers_mention_query(
    bot: DocBot, llm_client: MagicMock
) -> None:
    """A clean mention is answered via the LLM client and sent back.

    The mention token is stripped before the query is passed to the
    client, a typing indicator is shown, and the client's answer is sent
    back as a reply to the message.
    """
    llm_client.get_answer.return_value = "Use /setspawn to set the spawn."

    bot_user = bot.user
    assert bot_user is not None
    message, channel, tracker = _make_message(
        mentions=[bot_user],
        content=f"<@{_BOT_USER_ID}> how do I set the spawn point?",
    )
    await bot.on_message(message)

    llm_client.get_answer.assert_awaited_once_with(
        "how do I set the spawn point?",
    )
    message.reply.assert_awaited_once_with("Use /setspawn to set the spawn.")
    assert tracker.entered == 1
    assert tracker.exited == 1


@pytest.mark.asyncio
async def test_extract_query_strips_nickname_mention_form(
    bot: DocBot, llm_client: MagicMock
) -> None:
    """The ``<@!id>`` nickname-mention form is also stripped.

    Discord renders nickname mentions as ``<@!id>``; both forms must be
    removed so the remaining text is the user's question.
    """
    bot_user = bot.user
    assert bot_user is not None
    message, channel, tracker = _make_message(
        mentions=[bot_user],
        content=f"<@!{_BOT_USER_ID}> what is the max stack size?",
    )
    await bot.on_message(message)

    llm_client.get_answer.assert_awaited_once_with(
        "what is the max stack size?",
    )


@pytest.mark.asyncio
async def test_empty_query_after_strip_is_ignored(
    bot: DocBot, rate_limiter: MagicMock, llm_client: MagicMock
) -> None:
    """A mention with no accompanying text is silently dropped.

    The rate limiter has already recorded the attempt, but the LLM client
    is not invoked for an empty query and nothing is sent.
    """
    bot_user = bot.user
    assert bot_user is not None
    message, channel, tracker = _make_message(
        mentions=[bot_user], content=f"<@{_BOT_USER_ID}>   "
    )
    await bot.on_message(message)

    rate_limiter.is_allowed.assert_called_once()
    llm_client.get_answer.assert_not_called()
    message.reply.assert_not_called()
    assert tracker.entered == 0


@pytest.mark.asyncio
async def test_on_ready_logs_when_user_available(
    bot: DocBot, caplog: pytest.LogCaptureFixture
) -> None:
    """``on_ready`` logs the bot's tag and id when the user is known."""
    import logging

    caplog.set_level(logging.INFO, logger="bot.client")
    await bot.on_ready()

    assert any(
        "DocBot connected" in record.message for record in caplog.records
    )


@pytest.mark.asyncio
async def test_on_ready_tolerates_missing_user(
    rate_limiter: MagicMock, llm_client: MagicMock,
    caplog: pytest.LogCaptureFixture,
) -> None:
    """``on_ready`` does not raise when ``self.user`` is ``None``."""
    import logging

    client = DocBot(rate_limiter, llm_client)
    caplog.set_level(logging.INFO, logger="bot.client")
    # Should not raise.
    await client.on_ready()

    assert any(
        "DocBot connected" in record.message for record in caplog.records
    )


def test_constructor_stores_dependencies(
    rate_limiter: MagicMock, llm_client: MagicMock
) -> None:
    """The rate limiter and LLM client are stored as attributes."""
    client = DocBot(rate_limiter, llm_client)

    assert client.rate_limiter is rate_limiter
    assert client.llm_client is llm_client


def test_constructor_enables_message_content_intent(
    rate_limiter: MagicMock, llm_client: MagicMock
) -> None:
    """The default intents enable ``message_content`` for reading content."""
    client = DocBot(rate_limiter, llm_client)

    assert client.intents.message_content is True


def test_constructor_accepts_custom_intents(
    rate_limiter: MagicMock, llm_client: MagicMock
) -> None:
    """Explicitly passed intents are forwarded to the base client.

    :class:`discord.Client` normalizes the intents into its connection
    state, so the stored intents are compared by value rather than by
    identity. A distinctive flag (``guild_messages`` disabled) is used so
    a default-intents bot would not satisfy the assertion.
    """
    import discord

    custom = discord.Intents.default()
    custom.message_content = True
    custom.guild_messages = False  # differs from Intents.default()

    client = DocBot(rate_limiter, llm_client, intents=custom)

    assert client.intents == custom
    assert client.intents.value == custom.value
    assert client.intents.guild_messages is False
    assert client.intents.message_content is True


@pytest.mark.asyncio
async def test_answers_role_mention_query(
    bot: DocBot, llm_client: MagicMock
) -> None:
    """A query mentioning a role assigned to the bot is answered and sent back."""
    llm_client.get_answer.return_value = "Use /setspawn to set the spawn."

    # Create a mock role
    mock_role = MagicMock(name="role")
    mock_role.id = 777

    # Create the fake message
    message, channel, tracker = _make_message(
        mentions=[],
        content="<@&777> how do I set the spawn point?",
    )

    # Mock message.guild and message.guild.me and message.role_mentions
    mock_guild = MagicMock(name="guild")
    mock_me = MagicMock(name="me")
    mock_me.roles = [mock_role]
    mock_guild.me = mock_me
    message.guild = mock_guild
    message.role_mentions = [mock_role]

    await bot.on_message(message)

    llm_client.get_answer.assert_awaited_once_with(
        "how do I set the spawn point?",
    )
    message.reply.assert_awaited_once_with("Use /setspawn to set the spawn.")
    assert tracker.entered == 1
    assert tracker.exited == 1


def test_extract_query_strips_role_mentions(bot: DocBot) -> None:
    """Verify that _extract_query strips bot's role mentions but keeps others."""
    # Create mock roles
    role1 = MagicMock(name="role1")
    role1.id = 111
    role2 = MagicMock(name="role2")
    role2.id = 222

    # Create the fake message
    message, _, _ = _make_message(
        content="<@&111> <@&222> hello world <@&333>",
    )

    # Mock guild and me
    mock_guild = MagicMock(name="guild")
    mock_me = MagicMock(name="me")
    mock_me.roles = [role1, role2]
    mock_guild.me = mock_me
    message.guild = mock_guild

    # Call _extract_query directly
    query = bot._extract_query(message)
    assert query == "hello world <@&333>"
