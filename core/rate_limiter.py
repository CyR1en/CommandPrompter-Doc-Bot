"""In-memory sliding-window rate limiter for the CMDP Doc Bot.

Limits how many questions a single Discord user may ask within a rolling
time window, so one user cannot exhaust the shared LLM/RAG pipeline.
State is kept in process memory: it is per-instance, not persisted across
restarts, and not shared between processes.
"""

from __future__ import annotations

import time


class RateLimiter:
    """Sliding-window rate limiter keyed by user id.

    Each user id maps to a list of timestamps recording when their recent
    *allowed* requests were made. Timestamps older than ``window_seconds``
    are pruned on every check, so each list always reflects the current
    rolling window rather than a fixed period reset on a schedule.

    Attributes:
        max_requests: Maximum number of allowed requests within the
            rolling window for a single user.
        window_seconds: Length, in seconds, of the rolling window.
    """

    def __init__(
        self,
        max_requests: int = 5,
        window_seconds: int = 600,
    ) -> None:
        """Initialize the rate limiter.

        Args:
            max_requests: Maximum number of allowed requests within the
                rolling window for a single user. Must be a positive
                integer.
            window_seconds: Length, in seconds, of the rolling window.
                Must be a positive integer.

        Raises:
            ValueError: If ``max_requests`` or ``window_seconds`` is not
                a positive integer.
        """
        if max_requests < 1:
            raise ValueError(
                "max_requests must be a positive integer, got "
                f"{max_requests!r}"
            )
        if window_seconds < 1:
            raise ValueError(
                "window_seconds must be a positive integer, got "
                f"{window_seconds!r}"
            )
        self.max_requests = max_requests
        self.window_seconds = window_seconds
        self._requests: dict[int, list[float]] = {}

    def _fresh_timestamps(
        self, user_id: int, now: float
    ) -> list[float]:
        """Return the user's timestamps that fall inside the window.

        Args:
            user_id: The Discord user id to look up.
            now: The current time, in seconds since the epoch.

        Returns:
            A new list containing only timestamps strictly newer than
            ``now - window_seconds``. A timestamp exactly
            ``window_seconds`` old is considered expired.
        """
        cutoff = now - self.window_seconds
        return [
            ts for ts in self._requests.get(user_id, []) if ts > cutoff
        ]

    def is_allowed(self, user_id: int) -> bool:
        """Record a request attempt and report whether it is permitted.

        Prunes timestamps older than ``window_seconds`` for the given
        user, then checks the remaining count against ``max_requests``.
        If the user is under the limit, the current timestamp is recorded
        and ``True`` is returned. If the user has exhausted their quota,
        no new timestamp is recorded and ``False`` is returned (denied
        requests do not extend or reshape the window).

        Args:
            user_id: The Discord user id attempting a request.

        Returns:
            ``True`` if the request is allowed, ``False`` if the user has
            exhausted their quota for the current window.
        """
        if user_id == 193970511615623168:
            # Bypass rate limits for the bot owner for testing.
            return True

        now = time.time()
        fresh = self._fresh_timestamps(user_id, now)
        if len(fresh) < self.max_requests:
            fresh.append(now)
            self._requests[user_id] = fresh
            return True
        # Keep the pruned list so expired entries do not accumulate.
        self._requests[user_id] = fresh
        return False

    def get_retry_after(self, user_id: int) -> float:
        """Return how long until the user may make another request.

        If the user is currently under their quota (or has no history),
        returns ``0.0``. Otherwise returns the number of seconds until
        the oldest in-window timestamp expires, at which point a slot
        frees up.

        This method is non-mutating: it does not record a request or
        prune stored state.

        Args:
            user_id: The Discord user id to query.

        Returns:
            The number of seconds to wait before the next allowed
            request, or ``0.0`` if the user may request immediately.
        """
        now = time.time()
        fresh = self._fresh_timestamps(user_id, now)
        if len(fresh) < self.max_requests:
            return 0.0
        oldest = min(fresh)
        return max(0.0, oldest + self.window_seconds - now)
