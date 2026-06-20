"""Tests for :mod:`core.rate_limiter`."""

from __future__ import annotations

from collections.abc import Iterator
from unittest.mock import patch

import pytest

from core.rate_limiter import RateLimiter


class FakeClock:
    """Mutable fake replacement for :func:`time.time`.

    Allows tests to advance or set the current time deterministically.
    Instances are callable so they can be installed as the
    ``time.time`` replacement directly via :func:`unittest.mock.patch`
    using ``new=``.
    """

    def __init__(self, start: float = 0.0) -> None:
        """Initialize the clock at ``start`` seconds.

        Args:
            start: The initial clock value, in seconds.
        """
        self.now = start

    def __call__(self) -> float:
        """Return the current fake time."""
        return self.now

    def advance(self, seconds: float) -> None:
        """Advance the clock by ``seconds``.

        Args:
            seconds: The amount to add to the current time.
        """
        self.now += seconds

    def set(self, value: float) -> None:
        """Set the clock to an absolute time.

        Args:
            value: The new clock value, in seconds.
        """
        self.now = value


@pytest.fixture
def clock() -> Iterator[FakeClock]:
    """Yield a :class:`FakeClock` installed as ``time.time``."""
    fake = FakeClock()
    with patch("time.time", new=fake):
        yield fake


def test_first_request_allowed(clock: FakeClock) -> None:
    """A brand-new user's first request is always allowed."""
    limiter = RateLimiter()
    assert limiter.is_allowed(1) is True


def test_allows_up_to_max_requests(clock: FakeClock) -> None:
    """The first ``max_requests`` requests within the window pass."""
    limiter = RateLimiter(max_requests=5, window_seconds=600)
    for _ in range(5):
        assert limiter.is_allowed(42) is True


def test_denies_request_beyond_max_in_window(clock: FakeClock) -> None:
    """The ``max_requests + 1`` request inside the window is denied."""
    limiter = RateLimiter(max_requests=5, window_seconds=600)
    for _ in range(5):
        assert limiter.is_allowed(42) is True
    assert limiter.is_allowed(42) is False


def test_allows_again_after_window_expires(clock: FakeClock) -> None:
    """Once the window elapses, the quota resets."""
    limiter = RateLimiter(max_requests=5, window_seconds=600)
    for _ in range(5):
        limiter.is_allowed(7)
    clock.advance(600)
    assert limiter.is_allowed(7) is True


def test_partial_window_expiry_frees_slots(clock: FakeClock) -> None:
    """Only timestamps older than the window are pruned."""
    limiter = RateLimiter(max_requests=3, window_seconds=10)
    # Three requests at t=0, 1, 2 fill the quota.
    clock.set(0.0)
    assert limiter.is_allowed(9) is True
    clock.set(1.0)
    assert limiter.is_allowed(9) is True
    clock.set(2.0)
    assert limiter.is_allowed(9) is True
    # At t=2 the window is full: a fourth request is denied.
    assert limiter.is_allowed(9) is False
    # At t=10 the t=0 timestamp (exactly 10s old) has expired,
    # freeing one slot.
    clock.set(10.0)
    assert limiter.is_allowed(9) is True


def test_request_still_counted_just_inside_window(
    clock: FakeClock,
) -> None:
    """A timestamp just younger than the window still counts."""
    limiter = RateLimiter(max_requests=1, window_seconds=10)
    clock.set(0.0)
    assert limiter.is_allowed(1) is True
    clock.set(9.9)
    assert limiter.is_allowed(1) is False


def test_request_expires_at_exact_window_boundary(
    clock: FakeClock,
) -> None:
    """A timestamp exactly ``window_seconds`` old has expired."""
    limiter = RateLimiter(max_requests=1, window_seconds=10)
    clock.set(0.0)
    assert limiter.is_allowed(1) is True
    clock.set(10.0)
    assert limiter.is_allowed(1) is True


def test_users_are_independent(clock: FakeClock) -> None:
    """One user hitting their limit does not affect another."""
    limiter = RateLimiter(max_requests=2, window_seconds=600)
    assert limiter.is_allowed(1) is True
    assert limiter.is_allowed(1) is True
    assert limiter.is_allowed(1) is False
    # A different user still has their full quota.
    assert limiter.is_allowed(2) is True
    assert limiter.is_allowed(2) is True


def test_retry_after_zero_for_unknown_user(clock: FakeClock) -> None:
    """A user with no history may request immediately."""
    limiter = RateLimiter()
    assert limiter.get_retry_after(123) == 0.0


def test_retry_after_zero_when_under_limit(clock: FakeClock) -> None:
    """``retry_after`` is zero while the user is still under quota."""
    limiter = RateLimiter(max_requests=5, window_seconds=600)
    for _ in range(4):
        limiter.is_allowed(5)
    assert limiter.get_retry_after(5) == 0.0


def test_retry_after_when_limited(clock: FakeClock) -> None:
    """A limited user must wait for the oldest request to expire."""
    limiter = RateLimiter(max_requests=5, window_seconds=600)
    clock.set(0.0)
    for _ in range(5):
        limiter.is_allowed(5)
    clock.set(1.0)
    # Oldest request was at t=0; it expires at t=600; from t=1 that
    # is 599 seconds away.
    assert limiter.get_retry_after(5) == pytest.approx(599.0)


def test_retry_after_decreases_as_time_passes(
    clock: FakeClock,
) -> None:
    """``retry_after`` shrinks as the oldest request ages."""
    limiter = RateLimiter(max_requests=5, window_seconds=600)
    clock.set(0.0)
    for _ in range(5):
        limiter.is_allowed(5)
    clock.set(100.0)
    assert limiter.get_retry_after(5) == pytest.approx(500.0)
    clock.set(200.0)
    assert limiter.get_retry_after(5) == pytest.approx(400.0)


def test_retry_after_zero_after_window_expires(
    clock: FakeClock,
) -> None:
    """After the window elapses, ``retry_after`` returns to zero."""
    limiter = RateLimiter(max_requests=5, window_seconds=600)
    clock.set(0.0)
    for _ in range(5):
        limiter.is_allowed(5)
    clock.set(600.0)
    assert limiter.get_retry_after(5) == 0.0


def test_denied_request_does_not_add_timestamp(
    clock: FakeClock,
) -> None:
    """A denied request must not be recorded as an allowed request."""
    limiter = RateLimiter(max_requests=2, window_seconds=600)
    clock.set(0.0)
    assert limiter.is_allowed(1) is True
    assert limiter.is_allowed(1) is True
    assert limiter.is_allowed(1) is False
    # Only the two allowed requests were recorded.
    assert len(limiter._requests[1]) == 2


def test_default_limits() -> None:
    """Defaults are 5 requests per 600 seconds."""
    limiter = RateLimiter()
    assert limiter.max_requests == 5
    assert limiter.window_seconds == 600


def test_custom_limits() -> None:
    """Custom limits are honored."""
    limiter = RateLimiter(max_requests=3, window_seconds=120)
    assert limiter.max_requests == 3
    assert limiter.window_seconds == 120


@pytest.mark.parametrize("bad", [0, -1, -5])
def test_invalid_max_requests_raises(bad: int) -> None:
    """Non-positive ``max_requests`` is rejected."""
    with pytest.raises(ValueError):
        RateLimiter(max_requests=bad)


@pytest.mark.parametrize("bad", [0, -1, -5])
def test_invalid_window_seconds_raises(bad: int) -> None:
    """Non-positive ``window_seconds`` is rejected."""
    with pytest.raises(ValueError):
        RateLimiter(window_seconds=bad)
