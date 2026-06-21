"""Per-user opencode session lifecycle.

This module provides :class:`SessionManager`, an in-memory mapping from
Discord user id to opencode session id with a configurable idle TTL
(default 30 minutes). It is the glue between the Discord client (which
knows about users) and the :class:`core.opencode_client.OpencodeClient`
(which talks to the opencode server).

Why the bot keeps the mapping in memory
---------------------------------------

The opencode server persists sessions to
``~/.local/share/opencode/storage/`` (SQLite on current dev, JSON on
older versions) and they survive bot restart, opencode upgrade, and
machine reboot. The user → session mapping, however, is kept in
memory: on bot restart every user starts a fresh session. This is
deliberate — it avoids the bot reusing a session that was created
under a different model/provider/variant, and it keeps the mapping
simple (no persistence layer to maintain).

Per-user serialization
----------------------

Even though the opencode server serializes prompts on a given session
via its in-process ``Session.assertNotBusy`` lock, the bot holds an
:class:`asyncio.Lock` per Discord user id as well. This guards against
the same user firing two @mentions in quick succession (e.g. double
tap) before the first prompt has returned: the second call waits for
the first to finish, so the session's message stream stays coherent.
Different users' sessions are independent and can run in parallel.

Concurrency assumption
----------------------

All access happens on the bot's single event loop, so the internal
``dict`` is not wrapped in a lock. (An :class:`asyncio.Lock` would only
be needed if coroutines could be suspended mid-update, which they
cannot for plain dict reads/writes.)
"""

from __future__ import annotations

import asyncio
from collections.abc import Callable
from dataclasses import dataclass, field
from datetime import datetime, timedelta

from core.opencode_client import OpencodeClient
from core.logger import get_logger

_logger = get_logger(__name__)


@dataclass
class _SessionEntry:
    """In-memory record for one Discord user's opencode session.

    Attributes:
        session_id: The opencode session id (e.g. ``"ses_01J..."``).
        last_active: When the user last sent a message (used for TTL).
        lock: Per-user :class:`asyncio.Lock` used to serialize prompts
            for this user.
    """

    session_id: str
    last_active: datetime
    lock: asyncio.Lock = field(default_factory=asyncio.Lock)


class SessionManager:
    """Maps Discord user_id → opencode session_id with an idle TTL.

    The manager is constructed with an
    :class:`core.opencode_client.OpencodeClient` (used to create and
    delete sessions on the opencode server) and a TTL (default 30
    minutes). :meth:`get_or_create` returns the active session id for a
    user, creating a new one if there is no entry or the existing one
    has been idle past the TTL. :meth:`lock_for` returns a per-user
    :class:`asyncio.Lock` that callers should ``async with`` before
    issuing a prompt. :meth:`cleanup_expired` returns a snapshot of
    entries that have passed the TTL so a background sweeper can delete
    them on the server and :meth:`remove` them from the mapping.

    Attributes:
        client: The :class:`OpencodeClient` used to create/delete
            sessions on the opencode server.
        ttl: Idle TTL — a session that has not been touched for this
            long is eligible for cleanup.
        clock: Callable used to read the current time. Injected so
            tests can advance time without sleeping.
    """

    def __init__(
        self,
        *,
        client: OpencodeClient,
        ttl: timedelta = timedelta(minutes=30),
        clock: Callable[[], datetime] = datetime.now,
    ) -> None:
        """Initialize the session manager.

        Args:
            client: The :class:`OpencodeClient` used to create/delete
                sessions on the opencode server.
            ttl: Idle TTL — a session that has not been touched for
                this long is eligible for cleanup. Defaults to 30
                minutes.
            clock: Callable used to read the current time. Defaults to
                :func:`datetime.now`. Inject a fake clock in tests so
                they can advance time without sleeping.
        """
        self.client: OpencodeClient = client
        self.ttl: timedelta = ttl
        self.clock: Callable[[], datetime] = clock
        self._sessions: dict[int, _SessionEntry] = {}

    # ------------------------------------------------------------------
    # Lookup / creation
    # ------------------------------------------------------------------

    async def get_or_create(
        self,
        user_id: int,
        *,
        title: str,
        agent: str,
        provider_id: str | None,
        model_id: str | None,
    ) -> str:
        """Return the active session id for ``user_id``; create if needed.

        If there is an existing entry that has not yet passed the TTL,
        its session id is returned and ``last_active`` is refreshed. If
        the entry has expired, the old session is deleted on the server
        (so the user does not accumulate dead sessions in opencode's
        storage) and a new one is created. If there is no entry at all,
        a new session is created.

        Args:
            user_id: The Discord user id.
            title: Human-readable session title (e.g.
                ``f"discord:{user_id}"``).
            agent: Agent entry to use (e.g. ``"docbot"``).
            provider_id: Provider ID (e.g. ``"opencode"``). ``None`` is
                accepted for symmetry with :class:`LLMClient`'s
                permissive signature but the bot layer always passes a
                concrete value.
            model_id: Bare model id (e.g. ``"deepseek-v4-flash-free"``).
                ``None`` is accepted for symmetry but the bot layer
                always passes a concrete value.

        Returns:
            The session id (existing or newly created).
        """
        now: datetime = self.clock()
        entry: _SessionEntry | None = self._sessions.get(user_id)
        if entry is not None and entry.session_id:
            age: timedelta = now - entry.last_active
            if age < self.ttl:
                entry.last_active = now
                _logger.debug(
                    "Reusing session %s for user %s (age=%.1fs)",
                    entry.session_id,
                    user_id,
                    age.total_seconds(),
                )
                return entry.session_id
            # Expired: delete the old session on the server before
            # creating a new one so the user does not accumulate dead
            # sessions in opencode's storage.
            _logger.info(
                "Session %s for user %s expired (idle %.1fs > TTL %.0fs); "
                "deleting and creating a new one",
                entry.session_id,
                user_id,
                age.total_seconds(),
                self.ttl.total_seconds(),
            )
            try:
                await self.client.delete_session(entry.session_id)
            except Exception:
                _logger.warning(
                    "Failed to delete expired session %s for user %s; "
                    "creating a new one anyway",
                    entry.session_id,
                    user_id,
                    exc_info=True,
                )

        # Create a new session. The opencode server's ``CreateInput``
        # schema has no ``metadata`` field, so we just pass the title,
        # agent, and model. Per-user correlation is maintained in this
        # process's in-memory mapping (and survives as long as the bot
        # process does).
        session_id: str = await self.client.create_session(
            title=title,
            agent=agent,
            provider_id=provider_id or "",
            model_id=model_id or "",
        )
        self._sessions[user_id] = _SessionEntry(
            session_id=session_id,
            last_active=now,
        )
        _logger.info(
            "Created session %s for user %s", session_id, user_id
        )
        return session_id

    # ------------------------------------------------------------------
    # Per-user lock
    # ------------------------------------------------------------------

    def lock_for(self, user_id: int) -> asyncio.Lock:
        """Return the per-user lock (creating it if absent).

        The lock is created lazily and lives as long as the entry.
        Callers should ``async with`` it before issuing a prompt so the
        same user's prompts serialize (different users' sessions are
        independent and can run in parallel).

        Note:
            This method is not async — it does not need to suspend. The
            returned lock is acquired via ``await lock.acquire()`` or,
            more conveniently, ``async with session_manager.lock_for(
            user_id): ...``.

        Args:
            user_id: The Discord user id.

        Returns:
            The :class:`asyncio.Lock` for this user. The same lock
            object is returned each time for a given user (until the
            entry is :meth:`remove` d).
        """
        entry = self._sessions.get(user_id)
        if entry is None:
            # Create a placeholder entry so the lock is stable across
            # calls even before get_or_create has run. The session_id
            # is empty until get_or_create fills it in.
            entry = _SessionEntry(
                session_id="",
                last_active=self.clock(),
            )
            self._sessions[user_id] = entry
        return entry.lock

    # ------------------------------------------------------------------
    # Bookkeeping
    # ------------------------------------------------------------------

    def touch(self, user_id: int) -> None:
        """Update ``last_active`` to now. Idempotent.

        Called by the Discord client after a successful prompt so the
        TTL clock keeps rolling for an active conversation. If there is
        no entry for ``user_id`` (e.g. the entry was removed by the
        sweeper between get_or_create and the prompt returning), this
        is a no-op.

        Args:
            user_id: The Discord user id.
        """
        entry = self._sessions.get(user_id)
        if entry is not None:
            entry.last_active = self.clock()

    def cleanup_expired(self) -> list[tuple[int, str]]:
        """Return ``(user_id, session_id)`` pairs idle past the TTL.

        Provides a **snapshot** — does NOT mutate state. The caller (a
        background sweeper) is responsible for calling
        :meth:`OpencodeClient.delete_session` for each pair and then
        :meth:`remove` to drop the entry from the mapping. This
        two-step design keeps the server-side delete out of the
        critical section so a slow HTTP call does not block other
        users' lookups.

        Returns:
            A list of ``(user_id, session_id)`` pairs that have been
            idle past the TTL. Empty list when nothing is expired.
        """
        now: datetime = self.clock()
        expired: list[tuple[int, str]] = []
        for user_id, entry in self._sessions.items():
            if entry.session_id and (now - entry.last_active) >= self.ttl:
                expired.append((user_id, entry.session_id))
        return expired

    def remove(self, user_id: int, session_id: str | None = None) -> None:
        """Remove the entry for ``user_id`` (no-op if absent).

        Called by the sweeper after a successful (or best-effort)
        ``delete_session`` so the mapping does not keep pointing at a
        session that no longer exists on the server.

        If ``session_id`` is supplied, the entry is only removed when its
        current ``session_id`` still matches. This guards a race where
        the sweeper snapshots an expired entry, then a new message from
        the same user arrives and ``get_or_create`` replaces the entry
        with a fresh session — the sweeper's ``remove`` would otherwise
        clobber the fresh entry and orphan the new session on the
        server.

        Args:
            user_id: The Discord user id.
            session_id: If supplied, only remove the entry when its
                current session id still matches. ``None`` removes
                unconditionally (the historical behaviour).
        """
        if session_id is None:
            self._sessions.pop(user_id, None)
            return
        entry = self._sessions.get(user_id)
        if entry is not None and entry.session_id == session_id:
            del self._sessions[user_id]

    # ------------------------------------------------------------------
    # Introspection
    # ------------------------------------------------------------------

    def __len__(self) -> int:
        """Return the number of tracked users."""
        return len(self._sessions)

    def __contains__(self, user_id: int) -> bool:
        """Return ``True`` if ``user_id`` has a tracked session."""
        return user_id in self._sessions
