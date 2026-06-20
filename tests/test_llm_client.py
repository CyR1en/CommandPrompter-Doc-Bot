"""Tests for :mod:`core.llm_client`.

The ``opencode`` subprocess is replaced with a fake process object so the
tests exercise :meth:`LLMClient.get_answer` without spawning a real
``opencode`` CLI invocation. ``asyncio.create_subprocess_exec`` is patched
to return the fake process, whose ``communicate`` coroutine yields canned
stdout/stderr bytes.
"""

from __future__ import annotations

import json
from pathlib import Path
from unittest.mock import AsyncMock, patch

import pytest

from core.llm_client import LLMClient


class _FakeProc:
    """Fake :class:`asyncio.subprocess.Process` for testing.

    Attributes:
        returncode: The exit code exposed to the caller.
        _stdout: The bytes ``communicate`` returns as stdout.
        _stderr: The bytes ``communicate`` returns as stderr.
    """

    def __init__(
        self,
        stdout: bytes = b"",
        stderr: bytes = b"",
        returncode: int = 0,
    ) -> None:
        """Initialize the fake process with canned IO output.

        Args:
            stdout: Bytes to return from ``communicate`` as stdout.
            stderr: Bytes to return from ``communicate`` as stderr.
            returncode: Exit code to expose after ``communicate``.
        """
        self.returncode: int = returncode
        self._stdout: bytes = stdout
        self._stderr: bytes = stderr

    async def communicate(self) -> tuple[bytes, bytes]:
        """Return the canned stdout/stderr pair."""
        return self._stdout, self._stderr


def _make_stdout(*events: dict[str, object]) -> bytes:
    """Build newline-delimited JSON stdout from a sequence of events.

    Args:
        *events: Event dictionaries to encode, one per line.

    Returns:
        The JSON lines joined by newlines, encoded as UTF-8 bytes.
    """
    lines: list[str] = [json.dumps(event) for event in events]
    return "\n".join(lines).encode("utf-8")


@pytest.mark.asyncio
async def test_get_answer_concatenates_text_events() -> None:
    """All ``text`` events are concatenated in emission order.

    Non-text events (e.g. tool calls) are skipped.
    """
    stdout = _make_stdout(
        {"type": "text", "part": {"text": "Hello "}},
        {"type": "tool", "name": "grep"},
        {"type": "text", "part": {"text": "world!"}},
    )
    proc = _FakeProc(stdout=stdout)
    llm = LLMClient(working_dir=Path("/tmp/repos"))

    with patch(
        "core.llm_client.asyncio.create_subprocess_exec",
        new=AsyncMock(return_value=proc),
    ):
        answer = await llm.get_answer("how do I set spawn?")

    assert answer == "Hello world!"


@pytest.mark.asyncio
async def test_get_answer_returns_empty_string_when_no_text_events() -> None:
    """No ``text`` events yields an empty string."""
    stdout = _make_stdout(
        {"type": "tool", "name": "read"},
        {"type": "done"},
    )
    proc = _FakeProc(stdout=stdout)
    llm = LLMClient()

    with patch(
        "core.llm_client.asyncio.create_subprocess_exec",
        new=AsyncMock(return_value=proc),
    ):
        answer = await llm.get_answer("question?")

    assert answer == ""


@pytest.mark.asyncio
async def test_get_answer_returns_empty_string_on_empty_stdout() -> None:
    """Empty stdout yields an empty string."""
    proc = _FakeProc(stdout=b"")
    llm = LLMClient()

    with patch(
        "core.llm_client.asyncio.create_subprocess_exec",
        new=AsyncMock(return_value=proc),
    ):
        assert await llm.get_answer("q") == ""


@pytest.mark.asyncio
async def test_get_answer_skips_malformed_json_lines() -> None:
    """Non-JSON lines are skipped without aborting the parse."""
    stdout = (
        b'{"type":"text","part":{"text":"part 1 "}}\n'
        b"not json at all\n"
        b'{"type":"text","part":{"text":"part 2"}}\n'
    )
    proc = _FakeProc(stdout=stdout)
    llm = LLMClient()

    with patch(
        "core.llm_client.asyncio.create_subprocess_exec",
        new=AsyncMock(return_value=proc),
    ):
        answer = await llm.get_answer("q")

    assert answer == "part 1 part 2"


@pytest.mark.asyncio
async def test_get_answer_skips_blank_lines() -> None:
    """Blank lines in stdout are tolerated."""
    stdout = (
        b'\n{"type":"text","part":{"text":"A"}}\n\n\n'
        b'{"type":"text","part":{"text":"B"}}\n\n'
    )
    proc = _FakeProc(stdout=stdout)
    llm = LLMClient()

    with patch(
        "core.llm_client.asyncio.create_subprocess_exec",
        new=AsyncMock(return_value=proc),
    ):
        answer = await llm.get_answer("q")

    assert answer == "AB"


@pytest.mark.asyncio
async def test_get_answer_skips_text_event_with_non_string_text() -> None:
    """A ``text`` event whose ``text`` field is not a string is skipped."""
    stdout = _make_stdout(
        {"type": "text", "part": {"text": 123}},
        {"type": "text", "part": {"text": None}},
        {"type": "text", "part": {"text": "real"}},
    )
    proc = _FakeProc(stdout=stdout)
    llm = LLMClient()

    with patch(
        "core.llm_client.asyncio.create_subprocess_exec",
        new=AsyncMock(return_value=proc),
    ):
        answer = await llm.get_answer("q")

    assert answer == "real"


@pytest.mark.asyncio
async def test_get_answer_invokes_opencode_with_expected_args() -> None:
    """The subprocess is spawned with the configured agent, dir, and query."""
    proc = _FakeProc(stdout=_make_stdout({"type": "text", "part": {"text": "ans"}}))
    mock_exec = AsyncMock(return_value=proc)
    llm = LLMClient(
        agent="docbot",
        working_dir=Path("/srv/repos"),
        opencode_bin="opencode",
    )

    with patch("core.llm_client.asyncio.create_subprocess_exec", new=mock_exec):
        await llm.get_answer("what is the max stack?")

    mock_exec.assert_awaited_once()
    assert mock_exec.await_args is not None
    args = mock_exec.await_args.args
    # opencode run --agent docbot --format json --dir /srv/repos <query>
    assert args[0] == "opencode"
    assert args[1] == "run"
    assert "--agent" in args
    assert "docbot" in args
    assert "--format" in args
    assert "json" in args
    assert "--dir" in args
    assert "/srv/repos" in args
    assert "what is the max stack?\n\n Keep your answer less than 2000 characters." in args


@pytest.mark.asyncio
async def test_get_answer_uses_custom_opencode_bin() -> None:
    """A custom ``opencode_bin`` is used as the executable."""
    proc = _FakeProc(stdout=_make_stdout({"type": "text", "part": {"text": "x"}}))
    mock_exec = AsyncMock(return_value=proc)
    llm = LLMClient(opencode_bin="/usr/local/bin/opencode")

    with patch("core.llm_client.asyncio.create_subprocess_exec", new=mock_exec):
        await llm.get_answer("q")

    assert mock_exec.await_args is not None
    assert mock_exec.await_args.args[0] == "/usr/local/bin/opencode"


@pytest.mark.asyncio
async def test_get_answer_uses_custom_agent() -> None:
    """A custom agent name is passed via ``--agent``."""
    proc = _FakeProc(stdout=_make_stdout({"type": "text", "part": {"text": "x"}}))
    mock_exec = AsyncMock(return_value=proc)
    llm = LLMClient(agent="custom-agent")

    with patch("core.llm_client.asyncio.create_subprocess_exec", new=mock_exec):
        await llm.get_answer("q")

    assert mock_exec.await_args is not None
    args = mock_exec.await_args.args
    agent_idx = args.index("--agent")
    assert args[agent_idx + 1] == "custom-agent"


@pytest.mark.asyncio
async def test_get_answer_returns_text_on_nonzero_exit(
    caplog: pytest.LogCaptureFixture,
) -> None:
    """A non-zero exit code is logged as an error but text is still returned."""
    import logging

    proc = _FakeProc(
        stdout=_make_stdout({"type": "text", "part": {"text": "partial"}}),
        stderr=b"something went wrong",
        returncode=1,
    )
    llm = LLMClient()

    caplog.set_level(logging.ERROR, logger="core.llm_client")
    with patch(
        "core.llm_client.asyncio.create_subprocess_exec",
        new=AsyncMock(return_value=proc),
    ):
        answer = await llm.get_answer("q")

    assert answer == "partial"
    assert any(
        "code 1" in record.message for record in caplog.records
    )


def test_constructor_stores_attributes() -> None:
    """Agent, working_dir, and opencode_bin are stored as attributes."""
    llm = LLMClient(
        agent="my-agent",
        working_dir=Path("/data/repos"),
        opencode_bin="/opt/opencode",
    )

    assert llm.agent == "my-agent"
    assert llm.working_dir == Path("/data/repos")
    assert llm.opencode_bin == "/opt/opencode"


def test_constructor_defaults() -> None:
    """Defaults are ``docbot``, ``data/repos``, and ``opencode``."""
    llm = LLMClient()

    assert llm.agent == "docbot"
    assert llm.working_dir == Path("data/repos")
    assert llm.opencode_bin == "opencode"


def test_constructor_accepts_string_working_dir() -> None:
    """A string ``working_dir`` is normalized to a :class:`Path`."""
    llm = LLMClient(working_dir="/var/repos")

    assert llm.working_dir == Path("/var/repos")
    assert isinstance(llm.working_dir, Path)


def test_extract_text_concatenates_text_events() -> None:
    """``_extract_text`` concatenates ``text`` events from JSON lines."""
    stdout = _make_stdout(
        {"type": "text", "part": {"text": "A"}},
        {"type": "tool", "name": "bash"},
        {"type": "text", "part": {"text": "B"}},
    ).decode("utf-8")

    assert LLMClient._extract_text(stdout) == "AB"


def test_extract_text_returns_empty_on_no_text() -> None:
    """``_extract_text`` returns an empty string when there are no text events."""
    stdout = _make_stdout({"type": "done"}).decode("utf-8")

    assert LLMClient._extract_text(stdout) == ""


def test_extract_text_returns_empty_on_empty_string() -> None:
    """``_extract_text`` returns an empty string for empty input."""
    assert LLMClient._extract_text("") == ""


def test_extract_text_skips_malformed_lines() -> None:
    """Malformed JSON lines are skipped without raising."""
    stdout = '{"type":"text","part":{"text":"ok"}}\nGARBAGE\n{"type":"text","part":{"text":"!"}}'

    assert LLMClient._extract_text(stdout) == "ok!"
