"""Tests for :mod:`core.opencode_server`.

The ``opencode`` subprocess is replaced with a fake
:class:`asyncio.subprocess.Process` so the tests exercise
:class:`OpencodeServer.start` / :meth:`stop` without spawning a real
``opencode serve`` process. The HTTP ready-probe is patched so the
tests do not need a real server on the wire.
"""

from __future__ import annotations

import asyncio
import os
import signal
from pathlib import Path
from unittest.mock import AsyncMock, MagicMock, patch

import httpx
import pytest

from core.opencode_server import OpencodeServer, OpencodeServerError


class _FakeStream:
    """Fake :class:`asyncio.StreamReader` for stdout/stderr.

    Attributes:
        _lines: The lines (without trailing newline) to yield on
            ``readline``. Once exhausted, ``readline`` returns ``b""``
            (EOF).
    """

    def __init__(self, lines: list[bytes] | None = None) -> None:
        self._lines: list[bytes] = list(lines or [])

    async def readline(self) -> bytes:
        if self._lines:
            return self._lines.pop(0)
        return b""


class _FakeProc:
    """Fake :class:`asyncio.subprocess.Process` for testing.

    Attributes:
        returncode: ``None`` while the process is "running"; set to an
            int by :meth:`wait` or by a simulated signal.
        killed: Whether :meth:`kill` was called.
        signaled: List of signals sent via :meth:`send_signal`.
    """

    def __init__(
        self,
        *,
        stdout: _FakeStream | None = None,
        stderr: _FakeStream | None = None,
        returncode: int | None = None,
    ) -> None:
        self.stdout: _FakeStream = stdout or _FakeStream()
        self.stderr: _FakeStream = stderr or _FakeStream()
        self.returncode: int | None = returncode
        self.killed: bool = False
        self.signaled: list[int] = []
        self._wait_event: asyncio.Event = asyncio.Event()
        if returncode is not None:
            self._wait_event.set()

    def send_signal(self, sig: int) -> None:
        self.signaled.append(sig)
        if sig == signal.SIGTERM:
            # Simulate graceful exit after SIGTERM.
            self.returncode = 0
            self._wait_event.set()

    def kill(self) -> None:
        self.killed = True
        self.returncode = -9
        self._wait_event.set()

    async def wait(self) -> int:
        await self._wait_event.wait()
        assert self.returncode is not None
        return self.returncode


def _patch_exec(proc: _FakeProc) -> AsyncMock:
    """Return an :class:`AsyncMock` that patches ``create_subprocess_exec``.

    Args:
        proc: The fake process to return from the patched call.

    Returns:
        An :class:`AsyncMock` suitable for use as the
        ``create_subprocess_exec`` patch.
    """
    mock = AsyncMock(return_value=proc)
    return mock


@pytest.mark.asyncio
async def test_start_spawns_subprocess() -> None:
    """``start`` spawns ``opencode serve`` with the right args and env."""
    proc = _FakeProc(stdout=_FakeStream([b"listening\n"]))
    mock_exec = _patch_exec(proc)
    server = OpencodeServer(host="127.0.0.1", port=4242, password="pw")

    with patch(
        "core.opencode_server.asyncio.create_subprocess_exec", new=mock_exec
    ), patch(
        "core.opencode_server.httpx.AsyncClient"
    ) as mock_client_cls:
        # Make the ready probe succeed immediately.
        mock_client = MagicMock()
        mock_client.__aenter__ = AsyncMock(return_value=mock_client)
        mock_client.__aexit__ = AsyncMock(return_value=None)
        mock_client.get = AsyncMock(
            return_value=MagicMock(status_code=200)
        )
        mock_client_cls.return_value = mock_client
        await server.start()

    mock_exec.assert_awaited_once()
    args = mock_exec.await_args.args
    kwargs = mock_exec.await_args.kwargs
    assert args[0] == "opencode"
    assert "serve" in args
    assert "--port" in args
    assert "4242" in args
    assert "--hostname" in args
    assert "127.0.0.1" in args
    # The password is published to the subprocess env.
    env = kwargs.get("env") or {}
    assert env.get("OPENCODE_SERVER_PASSWORD") == "pw"
    assert server.is_running
    await server.stop()


@pytest.mark.asyncio
async def test_start_waits_for_ready() -> None:
    """The ready probe is retried until the server responds."""
    proc = _FakeProc()
    mock_exec = _patch_exec(proc)
    server = OpencodeServer(password="pw")

    call_count = 0

    async def fake_get(url):
        nonlocal call_count
        call_count += 1
        if call_count < 3:
            raise httpx.ConnectError("not up yet")
        return MagicMock(status_code=200)

    with patch(
        "core.opencode_server.asyncio.create_subprocess_exec", new=mock_exec
    ), patch(
        "core.opencode_server.httpx.AsyncClient"
    ) as mock_client_cls:
        mock_client = MagicMock()
        mock_client.__aenter__ = AsyncMock(return_value=mock_client)
        mock_client.__aexit__ = AsyncMock(return_value=None)
        mock_client.get = fake_get
        mock_client_cls.return_value = mock_client
        with patch("core.opencode_server.asyncio.sleep", new=AsyncMock()):
            await server.start()

    assert call_count >= 3
    assert server.is_running
    await server.stop()


@pytest.mark.asyncio
async def test_start_raises_if_not_ready_in_timeout() -> None:
    """A server that never becomes ready raises within the timeout."""
    proc = _FakeProc()
    mock_exec = _patch_exec(proc)
    server = OpencodeServer(password="pw")

    with patch(
        "core.opencode_server.asyncio.create_subprocess_exec", new=mock_exec
    ), patch(
        "core.opencode_server.httpx.AsyncClient"
    ) as mock_client_cls:
        mock_client = MagicMock()
        mock_client.__aenter__ = AsyncMock(return_value=mock_client)
        mock_client.__aexit__ = AsyncMock(return_value=None)
        # Always fail to connect.
        mock_client.get = AsyncMock(side_effect=httpx.ConnectError("nope"))
        mock_client_cls.return_value = mock_client
        # Make time advance fast so the deadline triggers immediately.
        loop = asyncio.get_event_loop()
        times = [0.0, 100.0]
        with patch(
            "core.opencode_server.asyncio.get_event_loop"
        ) as mock_loop:
            mock_loop.return_value.time.side_effect = times
            with patch(
                "core.opencode_server.asyncio.sleep", new=AsyncMock()
            ):
                with pytest.raises(OpencodeServerError):
                    await server.start(ready_timeout=1.0)


@pytest.mark.asyncio
async def test_start_raises_if_subprocess_exits_before_ready() -> None:
    """If the subprocess exits before the endpoint is up, an error is raised."""
    proc = _FakeProc(returncode=1)
    mock_exec = _patch_exec(proc)
    server = OpencodeServer(password="pw")

    with patch(
        "core.opencode_server.asyncio.create_subprocess_exec", new=mock_exec
    ), patch(
        "core.opencode_server.httpx.AsyncClient"
    ) as mock_client_cls:
        mock_client = MagicMock()
        mock_client.__aenter__ = AsyncMock(return_value=mock_client)
        mock_client.__aexit__ = AsyncMock(return_value=None)
        mock_client.get = AsyncMock(side_effect=httpx.ConnectError("nope"))
        mock_client_cls.return_value = mock_client
        with patch(
            "core.opencode_server.asyncio.sleep", new=AsyncMock()
        ):
            with pytest.raises(OpencodeServerError, match="exited before"):
                await server.start(ready_timeout=10.0)


@pytest.mark.asyncio
async def test_start_raises_if_binary_not_found() -> None:
    """A missing ``opencode`` binary raises :class:`OpencodeServerError`."""
    server = OpencodeServer(opencode_bin="/no/such/binary")

    with patch(
        "core.opencode_server.asyncio.create_subprocess_exec",
        new=AsyncMock(side_effect=FileNotFoundError("not found")),
    ):
        with pytest.raises(OpencodeServerError, match="not found"):
            await server.start()


@pytest.mark.asyncio
async def test_stop_terminates_subprocess() -> None:
    """``stop`` sends SIGTERM and waits for the process to exit."""
    proc = _FakeProc()
    mock_exec = _patch_exec(proc)
    server = OpencodeServer(password="pw")

    with patch(
        "core.opencode_server.asyncio.create_subprocess_exec", new=mock_exec
    ), patch(
        "core.opencode_server.httpx.AsyncClient"
    ) as mock_client_cls:
        mock_client = MagicMock()
        mock_client.__aenter__ = AsyncMock(return_value=mock_client)
        mock_client.__aexit__ = AsyncMock(return_value=None)
        mock_client.get = AsyncMock(
            return_value=MagicMock(status_code=200)
        )
        mock_client_cls.return_value = mock_client
        await server.start()

    await server.stop()

    assert signal.SIGTERM in proc.signaled
    assert server._proc is None
    assert not server.is_running


@pytest.mark.asyncio
async def test_stop_force_kills_on_timeout() -> None:
    """If SIGTERM does not exit the process in time, SIGKILL is sent."""
    proc = _FakeProc()
    # Override send_signal to NOT set the wait event (simulate
    # unresponsive process).
    def unresponsive_send(sig: int) -> None:
        proc.signaled.append(sig)

    proc.send_signal = unresponsive_send  # type: ignore[assignment]
    # ``wait`` never resolves on its own.
    proc._wait_event.clear()

    mock_exec = _patch_exec(proc)
    server = OpencodeServer(password="pw")

    with patch(
        "core.opencode_server.asyncio.create_subprocess_exec", new=mock_exec
    ), patch(
        "core.opencode_server.httpx.AsyncClient"
    ) as mock_client_cls:
        mock_client = MagicMock()
        mock_client.__aenter__ = AsyncMock(return_value=mock_client)
        mock_client.__aexit__ = AsyncMock(return_value=None)
        mock_client.get = AsyncMock(
            return_value=MagicMock(status_code=200)
        )
        mock_client_cls.return_value = mock_client
        await server.start()

    # Now make the process unresponsive to SIGTERM: the first
    # ``wait_for`` (after SIGTERM) times out; the second (after SIGKILL)
    # returns immediately.
    call_count = 0

    async def fake_wait_for(coro, timeout):
        nonlocal call_count
        call_count += 1
        coro.close()  # avoid "coroutine never awaited" warning
        if call_count == 1:
            # First wait (after SIGTERM) — simulate timeout.
            raise asyncio.TimeoutError()
        # Second wait (after SIGKILL) — simulate success.
        proc.returncode = -9
        return -9

    with patch(
        "core.opencode_server.asyncio.wait_for", new=fake_wait_for
    ):
        await server.stop()

    assert signal.SIGTERM in proc.signaled
    assert proc.killed
    assert server._proc is None


@pytest.mark.asyncio
async def test_stop_is_noop_when_not_started() -> None:
    """``stop`` on a never-started server is a no-op."""
    server = OpencodeServer(password="pw")
    # Should not raise.
    await server.stop()
    assert not server.is_running


@pytest.mark.asyncio
async def test_stop_is_noop_when_already_exited() -> None:
    """``stop`` on an already-exited process just logs output."""
    proc = _FakeProc()
    mock_exec = _patch_exec(proc)
    server = OpencodeServer(password="pw")

    with patch(
        "core.opencode_server.asyncio.create_subprocess_exec", new=mock_exec
    ), patch(
        "core.opencode_server.httpx.AsyncClient"
    ) as mock_client_cls:
        mock_client = MagicMock()
        mock_client.__aenter__ = AsyncMock(return_value=mock_client)
        mock_client.__aexit__ = AsyncMock(return_value=None)
        mock_client.get = AsyncMock(
            return_value=MagicMock(status_code=200)
        )
        mock_client_cls.return_value = mock_client
        await server.start()

    # Simulate the process having already exited after start() returned.
    proc.returncode = 0
    proc._wait_event.set()
    await server.stop()
    assert not server.is_running


def test_password_auto_generated_when_none() -> None:
    """A ``None`` password is auto-generated with secure randomness."""
    server = OpencodeServer()
    assert server.password
    # ``secrets.token_urlsafe(32)`` produces ~43 chars; just check it is
    # substantial and non-empty.
    assert len(server.password) >= 32


def test_password_used_when_supplied() -> None:
    """An explicit password is used verbatim."""
    server = OpencodeServer(password="my-secret")
    assert server.password == "my-secret"


def test_base_url_uses_configured_port() -> None:
    """``base_url`` is ``http://<host>:<port>``."""
    server = OpencodeServer(host="0.0.0.0", port=12345)
    assert server.base_url == "http://0.0.0.0:12345"


def test_is_running_false_before_start() -> None:
    """``is_running`` is ``False`` before :meth:`start`."""
    server = OpencodeServer()
    assert not server.is_running


@pytest.mark.asyncio
async def test_start_passes_cwd_to_subprocess() -> None:
    """The ``cwd`` kwarg is forwarded to ``create_subprocess_exec``."""
    proc = _FakeProc()
    mock_exec = _patch_exec(proc)
    server = OpencodeServer(password="pw", cwd=Path("/data/repos"))

    with patch(
        "core.opencode_server.asyncio.create_subprocess_exec", new=mock_exec
    ), patch(
        "core.opencode_server.httpx.AsyncClient"
    ) as mock_client_cls:
        mock_client = MagicMock()
        mock_client.__aenter__ = AsyncMock(return_value=mock_client)
        mock_client.__aexit__ = AsyncMock(return_value=None)
        mock_client.get = AsyncMock(
            return_value=MagicMock(status_code=200)
        )
        mock_client_cls.return_value = mock_client
        await server.start()

    kwargs = mock_exec.await_args.kwargs
    assert kwargs.get("cwd") == "/data/repos"
    await server.stop()


def test_log_output_redacts_password(
    caplog: pytest.LogCaptureFixture,
) -> None:
    """Captured subprocess stdout/stderr is redacted before logging.

    The ``opencode serve`` binary should not print the
    ``OPENCODE_SERVER_PASSWORD`` value, but we cannot rely on that.
    Redacting the password in the captured log is cheap insurance
    against an accidental leak to the bot's log file.
    """
    import logging

    server = OpencodeServer(password="super-secret-pw")
    server._stdout_chunks.append(
        "opencode listening with password=super-secret-pw"
    )
    server._stderr_chunks.append("debug: pw=super-secret-pw")

    with caplog.at_level(logging.INFO, logger="core.opencode_server"):
        server._log_output_on_shutdown()

    joined: str = "\n".join(record.message for record in caplog.records)
    assert "super-secret-pw" not in joined
    assert joined.count("***") >= 2  # both stdout and stderr were redacted
