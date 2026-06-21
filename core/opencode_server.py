"""Long-lived ``opencode serve`` subprocess lifecycle.

This module provides :class:`OpencodeServer`, an async manager that
spawns a single long-lived ``opencode serve`` subprocess at bot startup
and keeps it alive for the bot's lifetime. The bot's HTTP client
(:class:`core.opencode_client.OpencodeClient`) talks to the server's
HTTP API to create / continue / delete per-user sessions, which gives
each Discord user a persistent, stateful conversation with the docbot
agent.

Why a long-lived server instead of ``opencode run`` per query
-------------------------------------------------------------

The previous architecture spawned ``opencode run --session <id>`` per
query. Two problems forced the migration to ``opencode serve`` + HTTP:

1. ``opencode run --session <id>`` is **continue-only** — it
   hard-fails with "Session not found" if the session does not already
   exist, and there is no public ``opencode session create`` subcommand
   nor any way to specify a custom session ID (auto-generated IDs are
   ``ses_<26-char ULID>``). So per-user session continuity was not
   achievable via the CLI alone.

2. Two simultaneous ``opencode run --session <id>`` calls race: the
   in-process ``Session.assertNotBusy`` lock does not span subprocesses,
   so concurrent prompts on the same session corrupt the message stream
   (known issue #11699).

The server owns the in-process state authoritatively, so the
cross-subprocess race disappears and the HTTP API exposes a
``POST /session`` endpoint that creates a fresh session and returns its
auto-generated id.

Auth
----

``opencode serve`` uses HTTP Basic Auth. The username is always
``opencode``; the password is read from the ``OPENCODE_SERVER_PASSWORD``
env var at server start. When no password is supplied to this class a
secure random password is generated via :func:`secrets.token_urlsafe`
and published to the subprocess env so the server and this process
agree on it.

Working directory
-----------------

The server runs in whatever directory it was started in, and the
``opencode serve`` CLI (as of the current dev build) does not expose a
``--cwd`` flag. To preserve the bot's behaviour of searching
``data/repos/``, the caller may pass ``cwd=`` to :meth:`start` (and to
the constructor); the subprocess is spawned with that working directory
via ``asyncio.create_subprocess_exec``'s ``cwd=`` parameter, which is
equivalent to ``chdir`` before exec.
"""

from __future__ import annotations

import asyncio
import os
import secrets
import signal
from pathlib import Path

import httpx

from core.logger import get_logger

_logger = get_logger(__name__)

#: Grace period (seconds) after SIGTERM before escalating to SIGKILL.
_GRACE_SECONDS: float = 5.0

#: How long (seconds) to wait between ready-probe attempts.
_PROBE_INTERVAL: float = 0.25

#: Default opencode binary name (resolved via ``PATH``).
_DEFAULT_BINARY: str = "opencode"


class OpencodeServer:
    """Long-lived ``opencode serve`` subprocess manager.

    Spawns one ``opencode serve`` subprocess at startup, probes it until
    the HTTP endpoint is ready, and terminates it gracefully on
    :meth:`stop`. The bot's :class:`core.opencode_client.OpencodeClient`
    talks to the server via :attr:`base_url` using HTTP Basic Auth with
    :attr:`password`.

    Attributes:
        host: Hostname the server binds to (default ``"127.0.0.1"``).
        port: TCP port the server binds to (default ``4096``, the
            opencode default).
        password: HTTP Basic Auth password. Auto-generated via
            :func:`secrets.token_urlsafe(32)` when ``None`` is passed
            to the constructor.
        opencode_bin: Name or path of the ``opencode`` executable.
        cwd: Working directory the server is spawned in. The server's
            agent tools (``read``, ``grep``, ``list``, ``bash``) operate
            relative to this directory, so it should be the root that
            contains the cloned repositories (typically ``data/repos``).
    """

    def __init__(
        self,
        *,
        host: str = "127.0.0.1",
        port: int = 4096,
        password: str | None = None,
        opencode_bin: str = _DEFAULT_BINARY,
        cwd: Path | str | None = None,
    ) -> None:
        """Initialize the server manager.

        Args:
            host: Hostname the server binds to. Defaults to
                ``"127.0.0.1"`` (loopback only — the bot and the server
                run on the same host).
            port: TCP port the server binds to. Defaults to ``4096``
                (the opencode default).
            password: HTTP Basic Auth password. When ``None`` a secure
                random password is generated via
                :func:`secrets.token_urlsafe(32)` so each bot run uses a
                fresh password. Pass an explicit value to keep the same
                password across restarts (useful for attaching via
                ``opencode web`` for debugging).
            opencode_bin: Name or path of the ``opencode`` executable.
                Defaults to ``"opencode"`` (resolved via ``PATH``).
            cwd: Working directory the server is spawned in. Pass the
                repository root (e.g. ``data/repos``) so the agent's
                tools search the cloned repos. ``None`` means inherit
                the bot's working directory.
        """
        self.host: str = host
        self.port: int = port
        self.opencode_bin: str = opencode_bin
        self.cwd: Path | str | None = cwd
        self.password: str = password if password is not None else (
            secrets.token_urlsafe(32)
        )
        self._proc: asyncio.subprocess.Process | None = None
        self._stdout_chunks: list[str] = []
        self._stderr_chunks: list[str] = []

    # ------------------------------------------------------------------
    # Properties
    # ------------------------------------------------------------------

    @property
    def base_url(self) -> str:
        """The HTTP base URL the server is reachable at."""
        return f"http://{self.host}:{self.port}"

    @property
    def is_running(self) -> bool:
        """``True`` while the subprocess is alive (started and not stopped)."""
        proc = self._proc
        if proc is None:
            return False
        # ``returncode`` is ``None`` until the process exits, at which
        # point it is populated by the event loop. Querying it does not
        # block.
        try:
            return proc.returncode is None
        except ProcessLookupError:
            # The process has been reaped by a different code path.
            return False

    # ------------------------------------------------------------------
    # Lifecycle
    # ------------------------------------------------------------------

    async def start(self, *, ready_timeout: float = 30.0) -> None:
        """Spawn the subprocess and block until it is ready.

        Spawns ``opencode serve --port <port> --hostname <host>`` with
        ``OPENCODE_SERVER_PASSWORD`` set in the subprocess environment,
        then polls :attr:`base_url` until the server responds (or until
        ``ready_timeout`` seconds elapse).

        Args:
            ready_timeout: Maximum seconds to wait for the server to
                become ready. Defaults to ``30.0``.

        Raises:
            OpencodeServerError: If the subprocess cannot be spawned, if
                it exits before becoming ready, or if the HTTP endpoint
                does not respond within ``ready_timeout``.
        """
        if self._proc is not None and self.is_running:
            _logger.warning("opencode server already running; start() no-op")
            return

        env: dict[str, str] = dict(os.environ)
        env["OPENCODE_SERVER_PASSWORD"] = self.password

        cmd: list[str] = [
            self.opencode_bin,
            "serve",
            "--port",
            str(self.port),
            "--hostname",
            self.host,
        ]
        _logger.info(
            "Starting opencode server: %s (cwd=%s, host=%s, port=%s)",
            " ".join(cmd),
            self.cwd,
            self.host,
            self.port,
        )

        try:
            self._proc = await asyncio.create_subprocess_exec(
                *cmd,
                stdout=asyncio.subprocess.PIPE,
                stderr=asyncio.subprocess.PIPE,
                env=env,
                cwd=str(self.cwd) if self.cwd is not None else None,
            )
        except FileNotFoundError as exc:
            raise OpencodeServerError(
                f"opencode binary not found: {self.opencode_bin!r} "
                "(is it installed and on PATH?)"
            ) from exc

        # Drain stdout/stderr in the background so the subprocess's OS
        # pipe buffers do not fill up and block the server. The captured
        # lines are kept so :meth:`stop` can log them for diagnostics.
        assert self._proc.stdout is not None
        assert self._proc.stderr is not None
        asyncio.create_task(self._drain(self._proc.stdout, self._stdout_chunks))
        asyncio.create_task(self._drain(self._proc.stderr, self._stderr_chunks))

        await self._wait_until_ready(ready_timeout)
        _logger.info("opencode server ready at %s", self.base_url)

    async def _drain(
        self,
        stream: asyncio.StreamReader,
        sink: list[str],
    ) -> None:
        """Read lines from ``stream`` into ``sink`` until EOF.

        Keeps the subprocess's stdout/stderr OS pipe buffers from filling
        up and blocking the server, and retains the captured lines so
        :meth:`stop` can log them on shutdown for diagnostics.

        Args:
            stream: The subprocess stdout or stderr reader.
            sink: The list to append decoded lines to.
        """
        while True:
            line: bytes = await stream.readline()
            if not line:
                break
            sink.append(line.decode("utf-8", errors="replace").rstrip("\n"))

    async def _wait_until_ready(self, timeout: float) -> None:
        """Poll the server until it responds or the timeout elapses.

        Uses a short-timeout ``GET /session`` request (any 2xx or 4xx
        response counts as "ready"; only a connection error means the
        server is not up yet). If the subprocess exits before the
        endpoint responds, the captured stderr is surfaced.

        Args:
            timeout: Maximum seconds to wait.

        Raises:
            OpencodeServerError: If the subprocess exits before ready,
                or if the endpoint does not respond within ``timeout``.
        """
        deadline: float = asyncio.get_event_loop().time() + timeout
        probe_timeout: float = min(2.0, _PROBE_INTERVAL * 4)
        async with httpx.AsyncClient(
            base_url=self.base_url,
            auth=("opencode", self.password),
            timeout=probe_timeout,
        ) as probe:
            while True:
                if self._proc is not None and self._proc.returncode is not None:
                    raise OpencodeServerError(
                        "opencode server exited before becoming ready "
                        f"(exit={self._proc.returncode}); stderr: "
                        + "\n".join(self._stderr_chunks)[-2000:]
                    )
                if asyncio.get_event_loop().time() >= deadline:
                    raise OpencodeServerError(
                        f"opencode server did not become ready within "
                        f"{timeout}s at {self.base_url}; stderr: "
                        + "\n".join(self._stderr_chunks)[-2000:]
                    )
                try:
                    # ``GET /session`` requires auth; a 200 (empty list)
                    # or even a 401 (wrong password — should not happen
                    # here) both prove the HTTP server is up. Only a
                    # ``httpx.ConnectError`` means "not ready yet".
                    resp = await probe.get("/session")
                    # Any HTTP response means the server is up.
                    _logger.debug(
                        "opencode server ready probe: HTTP %s",
                        resp.status_code,
                    )
                    return
                except httpx.HTTPError as exc:
                    _logger.debug(
                        "opencode server not ready yet: %s", exc
                    )
                    await asyncio.sleep(_PROBE_INTERVAL)

    async def stop(self) -> None:
        """Terminate the subprocess gracefully; raise on failure.

        Sends ``SIGTERM`` and waits up to :data:`_GRACE_SECONDS` for the
        subprocess to exit. If it is still alive after the grace period,
        escalates to ``SIGKILL``. The captured stdout/stderr is logged
        at INFO level for diagnostics (the server's startup banner and
        any shutdown messages live there).

        Raises:
            OpencodeServerError: If the subprocess cannot be terminated
                (e.g. the PID has already been reaped by a different
                code path and ``ProcessLookupError`` is raised).
        """
        proc = self._proc
        if proc is None:
            return
        if proc.returncode is not None:
            # Already exited; just log the captured output.
            self._log_output_on_shutdown()
            self._proc = None
            return

        _logger.info("Stopping opencode server (SIGTERM)")
        try:
            proc.send_signal(signal.SIGTERM)
        except ProcessLookupError:
            self._proc = None
            return

        try:
            await asyncio.wait_for(proc.wait(), timeout=_GRACE_SECONDS)
        except asyncio.TimeoutError:
            _logger.warning(
                "opencode server did not exit within %.1fs; escalating to SIGKILL",
                _GRACE_SECONDS,
            )
            try:
                proc.kill()
            except ProcessLookupError:
                pass
            try:
                await asyncio.wait_for(proc.wait(), timeout=_GRACE_SECONDS)
            except asyncio.TimeoutError:  # pragma: no cover - extremely unlikely
                raise OpencodeServerError(
                    "opencode server did not exit after SIGKILL"
                )

        self._log_output_on_shutdown()
        self._proc = None

    def _log_output_on_shutdown(self) -> None:
        """Log the captured stdout/stderr for post-mortem diagnostics.

        The auto-generated HTTP Basic Auth password is redacted before
        logging — the ``opencode serve`` binary should not print it,
        but we cannot rely on that, and the cost of leaking a local-only
        secret to the bot's own log file is small but unnecessary.
        """
        for chunks, name in (
            (self._stdout_chunks, "stdout"),
            (self._stderr_chunks, "stderr"),
        ):
            if not chunks:
                continue
            text: str = "\n".join(chunks).replace(self.password, "***")
            _logger.info("opencode server %s:\n%s", name, text)


class OpencodeServerError(RuntimeError):
    """Raised when the opencode server fails to start or stop."""
