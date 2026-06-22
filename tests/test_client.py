"""Tests for :mod:`bot.client`.

The Discord :class:`~discord.Message`, :class:`~discord.User`, and
:class:`~discord.TextChannel` interfaces are replaced with lightweight
fakes so the tests exercise :class:`DocBot.on_message` without a Discord
gateway connection. The :class:`~core.rate_limiter.RateLimiter`,
:class:`~core.llm_client.LLMClient`, and
:class:`~core.session_manager.SessionManager` are mocked so the
rate-limiting, mention-checking, session-lookup, and LLM-invocation
branches can be asserted independently.

The :class:`DocBot` is constructed normally (its constructor does not
connect to Discord) and ``self.user`` is populated through the
``_connection.user`` backing attribute that the read-only ``Client.user``
property returns.
"""

from __future__ import annotations

import asyncio
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
        :class:`AsyncMock` returning ``"answer-text"`` and whose
        ``agent`` attribute is ``"docbot"``.
    """
    client = MagicMock(name="llm_client")
    client.get_answer = AsyncMock(return_value="answer-text")
    client.agent = "docbot"
    return client


@pytest.fixture()
def session_manager() -> MagicMock:
    """Return a mocked :class:`SessionManager`.

    Returns:
        A :class:`MagicMock` whose ``get_or_create`` is an
        :class:`AsyncMock` returning ``"ses_test"`` and whose
        ``lock_for`` returns a real, unlocked :class:`asyncio.Lock`.
        ``touch`` is a plain :class:`MagicMock`` (sync, no-op).
    """
    sm = MagicMock(name="session_manager")
    sm.get_or_create = AsyncMock(return_value="ses_test")
    # ``lock_for`` must return a real asyncio.Lock so ``async with`` works.
    sm.lock_for = MagicMock(return_value=asyncio.Lock())
    sm.touch = MagicMock(return_value=None)
    sm.remove = MagicMock(return_value=None)
    sm.cleanup_expired = MagicMock(return_value=[])
    return sm


@pytest.fixture()
def bot(
    rate_limiter: MagicMock,
    llm_client: MagicMock,
    session_manager: MagicMock,
) -> DocBot:
    """Return a :class:`DocBot` with mocked deps and a populated user.

    The bot is constructed normally (no gateway connection) and
    ``self.user`` is set via the ``_connection.user`` backing attribute
    so mention checks can succeed without logging in.

    Args:
        rate_limiter: The mocked rate limiter fixture.
        llm_client: The mocked LLM client fixture.
        session_manager: The mocked session manager fixture.

    Returns:
        A :class:`DocBot` whose ``self.user.id`` is :data:`_BOT_USER_ID`.
    """
    client = DocBot(
        rate_limiter,
        llm_client,
        session_manager,
        provider_id="opencode",
        model_id="deepseek-v4-flash-free",
        variant="max",
    )
    client._connection.user = _make_bot_user()  # type: ignore[attr-defined]
    return client


@pytest.mark.asyncio
async def test_ignores_messages_from_bots(
    bot: DocBot, rate_limiter: MagicMock, llm_client: MagicMock,
    session_manager: MagicMock,
) -> None:
    """Messages authored by bots are dropped before any logic runs.

    The rate limiter, session manager, and LLM client must not be
    consulted, and no reply is sent.
    """
    message, channel, tracker = _make_message(author_bot=True)
    await bot.on_message(message)

    rate_limiter.is_allowed.assert_not_called()
    session_manager.get_or_create.assert_not_called()
    llm_client.get_answer.assert_not_called()
    message.reply.assert_not_called()
    assert tracker.entered == 0


@pytest.mark.asyncio
async def test_ignores_messages_without_mention(
    bot: DocBot, rate_limiter: MagicMock, llm_client: MagicMock,
    session_manager: MagicMock,
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
    session_manager.get_or_create.assert_not_called()
    llm_client.get_answer.assert_not_called()
    message.reply.assert_not_called()
    assert tracker.entered == 0


@pytest.mark.asyncio
async def test_ignores_message_when_bot_user_unknown(
    rate_limiter: MagicMock, llm_client: MagicMock,
    session_manager: MagicMock,
) -> None:
    """When ``self.user`` is ``None`` mentions are never matched.

    Guards the early return so a pre-``on_ready`` message cannot raise.
    """
    client = DocBot(rate_limiter, llm_client, session_manager)
    # ``self.user`` stays None (no _connection.user set).
    message, channel, tracker = _make_message(
        mentions=[MagicMock()], content="hi"
    )
    await client.on_message(message)

    rate_limiter.is_allowed.assert_not_called()
    session_manager.get_or_create.assert_not_called()
    message.reply.assert_not_called()


@pytest.mark.asyncio
async def test_rate_limited_sends_rejection_and_skips_llm(
    bot: DocBot, rate_limiter: MagicMock, llm_client: MagicMock,
    session_manager: MagicMock,
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
    session_manager.get_or_create.assert_not_called()
    llm_client.get_answer.assert_not_called()
    assert tracker.entered == 0
    message.reply.assert_awaited_once()
    sent_text: str = message.reply.await_args.args[0]
    assert "42" in sent_text
    assert "second" in sent_text


@pytest.mark.asyncio
async def test_answers_mention_query(
    bot: DocBot, llm_client: MagicMock, session_manager: MagicMock
) -> None:
    """A clean mention is answered via the LLM client and sent back.

    The mention token is stripped before the query is passed to the
    client, a typing indicator is shown, the session is looked up, the
    per-user lock is acquired, and the client's answer is sent back as a
    reply to the message.
    """
    llm_client.get_answer.return_value = "Use /setspawn to set the spawn."

    bot_user = bot.user
    assert bot_user is not None
    message, channel, tracker = _make_message(
        mentions=[bot_user],
        content=f"<@{_BOT_USER_ID}> how do I set the spawn point?",
    )
    await bot.on_message(message)

    # Session was looked up.
    session_manager.get_or_create.assert_awaited_once()
    goc_kwargs = session_manager.get_or_create.await_args.kwargs
    assert goc_kwargs["title"] == "discord:42"
    assert goc_kwargs["agent"] == "docbot"
    assert goc_kwargs["provider_id"] == "opencode"
    assert goc_kwargs["model_id"] == "deepseek-v4-flash-free"
    # LLM was prompted with the session id and the stripped query.
    llm_client.get_answer.assert_awaited_once()
    ga_kwargs = llm_client.get_answer.await_args.kwargs
    assert ga_kwargs["session_id"] == "ses_test"
    assert ga_kwargs["provider_id"] == "opencode"
    assert ga_kwargs["model_id"] == "deepseek-v4-flash-free"
    assert ga_kwargs["variant"] == "max"
    # The query is the first positional arg, stripped of the mention.
    query_arg: str = llm_client.get_answer.await_args.args[0]
    assert query_arg == "how do I set the spawn point?"
    # Reply sent as an embed (the bot delivers answers as embeds so
    # long responses can span multiple pages without hitting
    # Discord's 2000-character plain-message cap).
    message.reply.assert_awaited_once()
    sent_embed = message.reply.await_args.kwargs["embed"]
    assert sent_embed.description == "Use /setspawn to set the spawn."
    assert tracker.entered == 1
    assert tracker.exited == 1
    # Session was touched on success.
    session_manager.touch.assert_called_once_with(42)


@pytest.mark.asyncio
async def test_extract_query_strips_nickname_mention_form(
    bot: DocBot, llm_client: MagicMock, session_manager: MagicMock
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

    query_arg: str = llm_client.get_answer.await_args.args[0]
    assert query_arg == "what is the max stack size?"


@pytest.mark.asyncio
async def test_empty_query_after_strip_is_ignored(
    bot: DocBot, rate_limiter: MagicMock, llm_client: MagicMock,
    session_manager: MagicMock,
) -> None:
    """A mention with no accompanying text is silently dropped.

    The rate limiter has already recorded the attempt, but the session
    manager and LLM client are not invoked for an empty query and
    nothing is sent.
    """
    bot_user = bot.user
    assert bot_user is not None
    message, channel, tracker = _make_message(
        mentions=[bot_user], content=f"<@{_BOT_USER_ID}>   "
    )
    await bot.on_message(message)

    rate_limiter.is_allowed.assert_called_once()
    session_manager.get_or_create.assert_not_called()
    llm_client.get_answer.assert_not_called()
    message.reply.assert_not_called()
    assert tracker.entered == 0


@pytest.mark.asyncio
async def test_sends_fallback_when_llm_returns_none(
    bot: DocBot, llm_client: MagicMock, session_manager: MagicMock
) -> None:
    """When ``get_answer`` returns ``None`` (HTTP failed) a fallback
    message is sent instead of an empty reply.

    Without this guard, ``message.reply("")`` would crash with
    ``discord.errors.HTTPException: 400 Bad Request: Cannot send an
    empty message`` and the user would see no response at all.
    """
    llm_client.get_answer.return_value = None

    bot_user = bot.user
    assert bot_user is not None
    message, channel, tracker = _make_message(
        mentions=[bot_user],
        content=f"<@{_BOT_USER_ID}> what is bigger, 9.8 or 9.11?",
    )
    await bot.on_message(message)

    llm_client.get_answer.assert_awaited_once()
    message.reply.assert_awaited_once()
    sent_text: str = message.reply.await_args.args[0]
    assert sent_text  # non-empty
    assert "couldn't generate" in sent_text.lower()
    # Session is NOT touched on failure.
    session_manager.touch.assert_not_called()


@pytest.mark.asyncio
async def test_sends_fallback_when_llm_returns_empty_string(
    bot: DocBot, llm_client: MagicMock, session_manager: MagicMock
) -> None:
    """When ``get_answer`` returns ``""`` (agent was silent) the bot
    also sends a fallback message.

    An empty-string answer is not a useful reply to the user; the
    fallback is the same polite "try again or rephrase" prompt used for
    HTTP failures.
    """
    llm_client.get_answer.return_value = ""

    bot_user = bot.user
    assert bot_user is not None
    message, channel, tracker = _make_message(
        mentions=[bot_user],
        content=f"<@{_BOT_USER_ID}> something off-topic",
    )
    await bot.on_message(message)

    llm_client.get_answer.assert_awaited_once()
    message.reply.assert_awaited_once()
    sent_text: str = message.reply.await_args.args[0]
    assert sent_text
    assert "couldn't generate" in sent_text.lower()
    # Session is NOT touched when the answer is empty.
    session_manager.touch.assert_not_called()


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
    session_manager: MagicMock,
    caplog: pytest.LogCaptureFixture,
) -> None:
    """``on_ready`` does not raise when ``self.user`` is ``None``."""
    import logging

    client = DocBot(rate_limiter, llm_client, session_manager)
    caplog.set_level(logging.INFO, logger="bot.client")
    # Should not raise.
    await client.on_ready()

    assert any(
        "DocBot connected" in record.message for record in caplog.records
    )


def test_constructor_stores_dependencies(
    rate_limiter: MagicMock, llm_client: MagicMock,
    session_manager: MagicMock,
) -> None:
    """The rate limiter, LLM client, and session manager are stored."""
    client = DocBot(
        rate_limiter, llm_client, session_manager,
        provider_id="opencode", model_id="m", variant="max",
    )

    assert client.rate_limiter is rate_limiter
    assert client.llm_client is llm_client
    assert client.session_manager is session_manager
    assert client.provider_id == "opencode"
    assert client.model_id == "m"
    assert client.variant == "max"


def test_constructor_enables_message_content_intent(
    rate_limiter: MagicMock, llm_client: MagicMock,
    session_manager: MagicMock,
) -> None:
    """The default intents enable ``message_content`` for reading content."""
    client = DocBot(rate_limiter, llm_client, session_manager)

    assert client.intents.message_content is True


def test_constructor_accepts_custom_intents(
    rate_limiter: MagicMock, llm_client: MagicMock,
    session_manager: MagicMock,
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

    client = DocBot(
        rate_limiter, llm_client, session_manager, intents=custom
    )

    assert client.intents == custom
    assert client.intents.value == custom.value
    assert client.intents.guild_messages is False
    assert client.intents.message_content is True


@pytest.mark.asyncio
async def test_answers_role_mention_query(
    bot: DocBot, llm_client: MagicMock, session_manager: MagicMock
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

    query_arg: str = llm_client.get_answer.await_args.args[0]
    assert query_arg == "how do I set the spawn point?"
    # Reply sent as an embed (see _send_embeds).
    message.reply.assert_awaited_once()
    sent_embed = message.reply.await_args.kwargs["embed"]
    assert sent_embed.description == "Use /setspawn to set the spawn."
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


# ---------------------------------------------------------------------------
# Per-user session flow
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_creates_session_for_new_user(
    bot: DocBot, llm_client: MagicMock, session_manager: MagicMock
) -> None:
    """``session_manager.get_or_create`` is called with the right args."""
    bot_user = bot.user
    assert bot_user is not None
    message, _, _ = _make_message(
        mentions=[bot_user], content=f"<@{_BOT_USER_ID}> hi"
    )
    await bot.on_message(message)

    session_manager.get_or_create.assert_awaited_once()
    args = session_manager.get_or_create.await_args.args
    kwargs = session_manager.get_or_create.await_args.kwargs
    # First positional arg is the user id.
    assert args[0] == 42
    assert kwargs["title"] == "discord:42"
    assert kwargs["agent"] == "docbot"


@pytest.mark.asyncio
async def test_reuses_session_within_ttl(
    bot: DocBot, llm_client: MagicMock, session_manager: MagicMock
) -> None:
    """A second message from the same user reuses the session.

    The ``get_or_create`` mock returns the same ``"ses_test"`` each
    time; the test asserts it is called twice (once per message) and
    the LLM client is prompted with the same session id both times.
    """
    bot_user = bot.user
    assert bot_user is not None
    msg1, _, _ = _make_message(
        mentions=[bot_user], content=f"<@{_BOT_USER_ID}> q1"
    )
    msg2, _, _ = _make_message(
        mentions=[bot_user], content=f"<@{_BOT_USER_ID}> q2"
    )
    await bot.on_message(msg1)
    await bot.on_message(msg2)

    assert session_manager.get_or_create.await_count == 2
    # Both prompts use the same session id.
    sid1 = llm_client.get_answer.await_args_list[0].kwargs["session_id"]
    sid2 = llm_client.get_answer.await_args_list[1].kwargs["session_id"]
    assert sid1 == sid2 == "ses_test"


@pytest.mark.asyncio
async def test_creates_new_session_after_ttl_expires(
    bot: DocBot, llm_client: MagicMock, session_manager: MagicMock
) -> None:
    """After the sweeper runs, a new message gets a new session.

    Simulates the sweeper by making ``get_or_create`` return a different
    session id on the second call (the real :class:`SessionManager`
    would do this after the TTL expired and the sweeper removed the
    entry).
    """
    bot_user = bot.user
    assert bot_user is not None
    msg1, _, _ = _make_message(
        mentions=[bot_user], content=f"<@{_BOT_USER_ID}> q1"
    )
    msg2, _, _ = _make_message(
        mentions=[bot_user], content=f"<@{_BOT_USER_ID}> q2"
    )

    session_manager.get_or_create = AsyncMock(
        side_effect=["ses_old", "ses_new"]
    )
    await bot.on_message(msg1)
    await bot.on_message(msg2)

    sid1 = llm_client.get_answer.await_args_list[0].kwargs["session_id"]
    sid2 = llm_client.get_answer.await_args_list[1].kwargs["session_id"]
    assert sid1 == "ses_old"
    assert sid2 == "ses_new"


@pytest.mark.asyncio
async def test_acquires_per_user_lock(
    bot: DocBot, llm_client: MagicMock, session_manager: MagicMock
) -> None:
    """The per-user lock is acquired BEFORE ``get_or_create`` and held
    through ``get_answer``.

    Regression guard for the C3 race: ``get_or_create`` must be inside
    the lock's ``async with`` block, otherwise two concurrent @mentions
    for the same new user would both pass the existence check and each
    create a separate session on the server (orphaning one). Verified
    by recording the order of ``session_manager`` calls and asserting
    ``lock_for`` precedes ``get_or_create``.
    """
    lock = asyncio.Lock()
    session_manager.lock_for = MagicMock(return_value=lock)
    bot_user = bot.user
    assert bot_user is not None
    message, _, _ = _make_message(
        mentions=[bot_user], content=f"<@{_BOT_USER_ID}> hi"
    )
    await bot.on_message(message)

    session_manager.lock_for.assert_called_once_with(42)
    # The lock must be released after the prompt returns.
    assert not lock.locked()
    # ``lock_for`` is called BEFORE ``get_or_create`` so two concurrent
    # messages for the same user serialize through the lock rather than
    # both racing through the existence check.
    lock_for_call_order: int = (
        session_manager.lock_for.call_args_list[0].__hash__()
    )
    get_or_create_calls: list = (
        session_manager.get_or_create.call_args_list
    )
    assert get_or_create_calls, "get_or_create should have been called"
    # Parent manager mock records the call order; the simplest check is
    # that ``lock_for`` was awaited before ``get_or_create`` (its
    # ``__call__`` happens first).
    manager_call_names: list[str] = [
        c[0] for c in session_manager.method_calls
    ]
    lock_idx: int = manager_call_names.index("lock_for")
    create_idx: int = manager_call_names.index("get_or_create")
    assert lock_idx < create_idx, (
        "lock_for must be called before get_or_create "
        f"(got {manager_call_names})"
    )


@pytest.mark.asyncio
async def test_touches_session_on_success(
    bot: DocBot, llm_client: MagicMock, session_manager: MagicMock
) -> None:
    """``session_manager.touch`` is called when the answer is non-empty."""
    llm_client.get_answer.return_value = "a real answer"
    bot_user = bot.user
    assert bot_user is not None
    message, _, _ = _make_message(
        mentions=[bot_user], content=f"<@{_BOT_USER_ID}> hi"
    )
    await bot.on_message(message)

    session_manager.touch.assert_called_once_with(42)


@pytest.mark.asyncio
async def test_does_not_touch_session_on_failure(
    bot: DocBot, llm_client: MagicMock, session_manager: MagicMock
) -> None:
    """``session_manager.touch`` is NOT called when ``get_answer`` returns None."""
    llm_client.get_answer.return_value = None
    bot_user = bot.user
    assert bot_user is not None
    message, _, _ = _make_message(
        mentions=[bot_user], content=f"<@{_BOT_USER_ID}> hi"
    )
    await bot.on_message(message)

    session_manager.touch.assert_not_called()


@pytest.mark.asyncio
async def test_does_not_touch_session_on_empty_answer(
    bot: DocBot, llm_client: MagicMock, session_manager: MagicMock
) -> None:
    """``touch`` is NOT called when ``get_answer`` returns ``""``."""
    llm_client.get_answer.return_value = ""
    bot_user = bot.user
    assert bot_user is not None
    message, _, _ = _make_message(
        mentions=[bot_user], content=f"<@{_BOT_USER_ID}> hi"
    )
    await bot.on_message(message)

    session_manager.touch.assert_not_called()
