"""Tests for :mod:`core.session_manager`.

The :class:`OpencodeClient` is replaced with a
:class:`FakeOpencodeClient` that records every call and returns canned
session ids, so the tests exercise :class:`SessionManager` without a
real ``opencode serve`` server. A fake clock is injected so the TTL
logic can be tested without sleeping.
"""

from __future__ import annotations

import asyncio
from datetime import datetime, timedelta
from typing import Any

import pytest

from core.opencode_client import OpencodeClientError
from core.session_manager import SessionManager


class FakeOpencodeClient:
    """Fake :class:`core.opencode_client.OpencodeClient` for testing.

    Records every call so tests can assert on the sequence of
    operations. ``create_session`` returns incrementing session ids
    (``"ses_0"``, ``"ses_1"``, ...).

    Attributes:
        create_calls: List of kwargs dicts passed to ``create_session``.
        delete_calls: List of session ids passed to ``delete_session``.
        prompt_calls: List of kwargs dicts passed to ``prompt``.
        next_id: The next session id to return from
            ``create_session``.
        delete_error: Optional exception to raise from
            ``delete_session`` (for testing the best-effort cleanup).
    """

    def __init__(self) -> None:
        self.create_calls: list[dict[str, Any]] = []
        self.delete_calls: list[str] = []
        self.prompt_calls: list[dict[str, Any]] = []
        self.next_id: int = 0
        self.delete_error: Exception | None = None

    async def create_session(
        self,
        *,
        title: str,
        agent: str,
        provider_id: str,
        model_id: str,
    ) -> str:
        self.create_calls.append(
            {
                "title": title,
                "agent": agent,
                "provider_id": provider_id,
                "model_id": model_id,
            }
        )
        sid: str = f"ses_{self.next_id}"
        self.next_id += 1
        return sid

    async def delete_session(self, session_id: str) -> None:
        self.delete_calls.append(session_id)
        if self.delete_error is not None:
            raise self.delete_error

    async def prompt(
        self,
        *,
        session_id: str,
        parts: list[dict[str, object]],
        agent: str,
        provider_id: str,
        model_id: str,
        variant: str | None = None,
    ) -> str:
        self.prompt_calls.append(
            {
                "session_id": session_id,
                "parts": parts,
                "agent": agent,
                "provider_id": provider_id,
                "model_id": model_id,
                "variant": variant,
            }
        )
        return "fake answer"

    async def close(self) -> None:
        pass


def _make_manager(
    *,
    client: FakeOpencodeClient | None = None,
    ttl: timedelta = timedelta(minutes=30),
    clock: Any = None,
) -> SessionManager:
    """Build a :class:`SessionManager` with a fake client and clock.

    Args:
        client: The fake client to wire in. A fresh one is created if
            omitted.
        ttl: Idle TTL. Defaults to 30 minutes.
        clock: The clock callable. Defaults to a mutable
            :class:`_FakeClock` at time ``2000-01-01 00:00``.

    Returns:
        A :class:`SessionManager` wired with the fakes.
    """
    if client is None:
        client = FakeOpencodeClient()
    if clock is None:
        clock = _FakeClock()
    return SessionManager(client=client, ttl=ttl, clock=clock)


class _FakeClock:
    """Mutable clock for tests.

    Attributes:
        now: The current time (returned when called).
    """

    def __init__(self, start: datetime | None = None) -> None:
        self.now: datetime = start or datetime(2000, 1, 1, 0, 0, 0)

    def __call__(self) -> datetime:
        return self.now

    def advance(self, **kwargs: Any) -> None:
        """Advance the clock by ``timedelta(**kwargs)``."""
        self.now = self.now + timedelta(**kwargs)


# ---------------------------------------------------------------------------
# get_or_create
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_get_or_create_creates_for_new_user() -> None:
    """A new user triggers a ``create_session`` call and the id is stored."""
    client = FakeOpencodeClient()
    clock = _FakeClock()
    manager = SessionManager(client=client, clock=clock)

    sid = await manager.get_or_create(
        42,
        title="discord:42",
        agent="docbot",
        provider_id="opencode",
        model_id="deepseek-v4-flash-free",
    )

    assert sid == "ses_0"
    assert len(client.create_calls) == 1
    call = client.create_calls[0]
    assert call["title"] == "discord:42"
    assert call["agent"] == "docbot"
    assert call["provider_id"] == "opencode"
    assert call["model_id"] == "deepseek-v4-flash-free"
    assert 42 in manager


@pytest.mark.asyncio
async def test_get_or_create_returns_existing_within_ttl() -> None:
    """A second call within the TTL reuses the existing session."""
    client = FakeOpencodeClient()
    clock = _FakeClock()
    manager = SessionManager(
        client=client, ttl=timedelta(minutes=30), clock=clock
    )

    sid1 = await manager.get_or_create(
        42, title="t", agent="docbot", provider_id="opencode", model_id="m"
    )
    # Advance 29 minutes — still within the 30-min TTL.
    clock.advance(minutes=29)
    sid2 = await manager.get_or_create(
        42, title="t", agent="docbot", provider_id="opencode", model_id="m"
    )

    assert sid1 == sid2
    assert len(client.create_calls) == 1
    assert len(client.delete_calls) == 0


@pytest.mark.asyncio
async def test_get_or_create_creates_new_after_ttl_expires() -> None:
    """After the TTL the old session is deleted and a new one created."""
    client = FakeOpencodeClient()
    clock = _FakeClock()
    manager = SessionManager(
        client=client, ttl=timedelta(minutes=30), clock=clock
    )

    sid1 = await manager.get_or_create(
        42, title="t", agent="docbot", provider_id="opencode", model_id="m"
    )
    # Advance 31 minutes — past the TTL.
    clock.advance(minutes=31)
    sid2 = await manager.get_or_create(
        42, title="t", agent="docbot", provider_id="opencode", model_id="m"
    )

    assert sid1 != sid2
    assert len(client.create_calls) == 2
    assert client.delete_calls == [sid1]


@pytest.mark.asyncio
async def test_get_or_create_does_not_send_metadata() -> None:
    """``get_or_create`` does not pass a ``metadata`` field to the server.

    Regression guard: the opencode server's ``CreateInput`` schema has
    no ``metadata`` field, so passing one caused ``400 Bad Request``.
    Per-user correlation lives in the in-memory mapping now (and
    survives only as long as the bot process).
    """
    client = FakeOpencodeClient()
    manager = _make_manager(client=client)

    await manager.get_or_create(
        193970511615623168,
        title="discord:193970511615623168",
        agent="docbot",
        provider_id="opencode",
        model_id="m",
    )

    create_call: dict[str, Any] = client.create_calls[0]
    assert "metadata" not in create_call
    assert create_call["title"] == "discord:193970511615623168"


@pytest.mark.asyncio
async def test_get_or_create_continues_on_delete_failure() -> None:
    """A failed delete of the expired session does not block re-creation."""
    client = FakeOpencodeClient()
    client.delete_error = OpencodeClientError("boom")
    clock = _FakeClock()
    manager = SessionManager(
        client=client, ttl=timedelta(minutes=30), clock=clock
    )

    sid1 = await manager.get_or_create(
        42, title="t", agent="docbot", provider_id="opencode", model_id="m"
    )
    clock.advance(minutes=31)
    sid2 = await manager.get_or_create(
        42, title="t", agent="docbot", provider_id="opencode", model_id="m"
    )

    assert sid1 != sid2
    assert len(client.delete_calls) == 1
    assert len(client.create_calls) == 2


# ---------------------------------------------------------------------------
# touch
# ---------------------------------------------------------------------------


def test_touch_updates_last_active() -> None:
    """``touch`` refreshes ``last_active`` to the current clock time."""
    clock = _FakeClock()
    manager = _make_manager(clock=clock)

    # No entry yet — touch is a no-op.
    manager.touch(42)
    assert 42 not in manager

    # Insert an entry via the internal dict for unit-test purposes.
    # (In production, get_or_create is the only insertion path.)
    from core.session_manager import _SessionEntry

    manager._sessions[42] = _SessionEntry(
        session_id="ses_x", last_active=clock.now
    )
    clock.advance(minutes=10)
    manager.touch(42)
    assert manager._sessions[42].last_active == clock.now


# ---------------------------------------------------------------------------
# cleanup_expired
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_cleanup_expired_returns_idle_users() -> None:
    """Users idle past the TTL appear in the expired snapshot."""
    clock = _FakeClock()
    manager = SessionManager(
        client=FakeOpencodeClient(),
        ttl=timedelta(minutes=30),
        clock=clock,
    )

    await manager.get_or_create(
        1, title="t", agent="a", provider_id="p", model_id="m"
    )
    clock.advance(minutes=10)
    await manager.get_or_create(
        2, title="t", agent="a", provider_id="p", model_id="m"
    )
    # User 1 is now 10+? minutes old, user 2 is fresh.
    # Advance 25 minutes: user 1 is 35 min (expired), user 2 is 25 min (ok).
    clock.advance(minutes=25)

    expired = manager.cleanup_expired()
    expired_ids = {uid for uid, _ in expired}
    assert 1 in expired_ids
    assert 2 not in expired_ids


def test_cleanup_expired_keeps_active_users() -> None:
    """Users within the TTL are NOT in the expired snapshot."""
    clock = _FakeClock()
    manager = SessionManager(
        client=FakeOpencodeClient(),
        ttl=timedelta(minutes=30),
        clock=clock,
    )
    from core.session_manager import _SessionEntry

    manager._sessions[1] = _SessionEntry(
        session_id="ses_1", last_active=clock.now
    )
    clock.advance(minutes=29)
    expired = manager.cleanup_expired()
    assert expired == []


def test_cleanup_expired_is_a_snapshot() -> None:
    """``cleanup_expired`` does not mutate state."""
    clock = _FakeClock()
    manager = SessionManager(
        client=FakeOpencodeClient(),
        ttl=timedelta(minutes=30),
        clock=clock,
    )
    from core.session_manager import _SessionEntry

    manager._sessions[1] = _SessionEntry(
        session_id="ses_1", last_active=clock.now
    )
    clock.advance(minutes=31)
    expired = manager.cleanup_expired()
    assert len(expired) == 1
    # The entry is still in the mapping — only ``remove`` drops it.
    assert 1 in manager


# ---------------------------------------------------------------------------
# remove
# ---------------------------------------------------------------------------


def test_remove_drops_entry() -> None:
    """``remove`` deletes the entry (no-op if absent)."""
    clock = _FakeClock()
    manager = _make_manager(clock=clock)
    from core.session_manager import _SessionEntry

    manager._sessions[1] = _SessionEntry(
        session_id="ses_1", last_active=clock.now
    )
    assert 1 in manager
    manager.remove(1)
    assert 1 not in manager
    # Removing a non-existent entry is a no-op.
    manager.remove(999)


def test_remove_with_matching_session_id_removes_entry() -> None:
    """``remove(user_id, session_id)`` removes the entry when the stored
    ``session_id`` still matches.

    This is the happy path the sweeper takes after a successful
    ``client.delete_session``.
    """
    clock = _FakeClock()
    manager = _make_manager(clock=clock)
    from core.session_manager import _SessionEntry

    manager._sessions[1] = _SessionEntry(
        session_id="ses_abc", last_active=clock.now
    )
    manager.remove(1, "ses_abc")
    assert 1 not in manager


def test_remove_with_non_matching_session_id_keeps_entry() -> None:
    """``remove(user_id, session_id)`` is a no-op when the stored
    ``session_id`` does not match.

    Regression guard for the C2 race: the sweeper snapshots an expired
    entry ``(42, "ses_old")``, then the user sends a new message and
    ``get_or_create`` replaces the entry with ``(42, "ses_new")``. When
    the sweeper finally calls ``remove(42, "ses_old")``, the entry must
    be preserved because the stale session_id no longer matches.
    """
    clock = _FakeClock()
    manager = _make_manager(clock=clock)
    from core.session_manager import _SessionEntry

    manager._sessions[42] = _SessionEntry(
        session_id="ses_new", last_active=clock.now
    )
    manager.remove(42, "ses_old")
    assert 42 in manager
    assert manager._sessions[42].session_id == "ses_new"


# ---------------------------------------------------------------------------
# lock_for
# ---------------------------------------------------------------------------


def test_lock_for_returns_distinct_locks_per_user() -> None:
    """Different users get different :class:`asyncio.Lock` objects."""
    manager = _make_manager()
    lock1 = manager.lock_for(1)
    lock2 = manager.lock_for(2)
    assert lock1 is not lock2


def test_lock_for_returns_same_lock_for_same_user() -> None:
    """The same user gets the same lock object on every call."""
    manager = _make_manager()
    lock1 = manager.lock_for(1)
    lock2 = manager.lock_for(1)
    assert lock1 is lock2


@pytest.mark.asyncio
async def test_concurrent_calls_serialize() -> None:
    """Two coroutines holding the same lock wait for each other.

    Acquires the per-user lock in one coroutine, then verifies a second
    coroutine cannot acquire it until the first releases.
    """
    manager = _make_manager()
    lock = manager.lock_for(1)

    order: list[str] = []

    async def hold():
        async with lock:
            order.append("a acquired")
            await asyncio.sleep(0.05)
            order.append("a released")

    async def wait():
        # Wait a beat so ``hold`` acquires first.
        await asyncio.sleep(0.01)
        async with lock:
            order.append("b acquired")
            order.append("b released")

    await asyncio.gather(hold(), wait())

    # ``a`` must acquire before ``b``; ``b`` cannot acquire until
    # ``a`` releases.
    assert order == [
        "a acquired",
        "a released",
        "b acquired",
        "b released",
    ]


# ---------------------------------------------------------------------------
# Introspection
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_len_and_contains() -> None:
    """``__len__`` and ``__contains__`` reflect tracked users."""
    manager = _make_manager()
    assert len(manager) == 0
    assert 42 not in manager

    await manager.get_or_create(
        42, title="t", agent="a", provider_id="p", model_id="m"
    )
    assert len(manager) == 1
    assert 42 in manager
