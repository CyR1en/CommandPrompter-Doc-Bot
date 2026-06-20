"""OpenCode-backed LLM client for the CMDP Doc Bot.

This module provides :class:`LLMClient`, an async wrapper around the
``opencode`` CLI. Instead of calling an OpenAI-compatible endpoint
directly, each :meth:`get_answer` call spawns an ``opencode run``
subprocess configured with the ``docbot`` agent. OpenCode then uses its
tools (``read``, ``grep``, ``bash``, ``list``) to search the cloned
repositories under ``working_dir`` and streams back JSON event lines; the
client extracts the ``text`` events and concatenates them into the final
answer.

The persona and repository context are handled entirely by OpenCode: the
``docbot`` agent definition (copied to
``~/.config/opencode/agents/docbot.md`` by
:func:`core.opencode_config.setup_opencode`) carries the system prompt,
and the repos are read on demand from ``working_dir``.
"""

from __future__ import annotations

import asyncio
import json
from pathlib import Path

from core.logger import get_logger

_logger = get_logger(__name__)


class LLMClient:
    """Async client that answers queries by spawning an ``opencode`` subprocess.

    The client is configured with the OpenCode agent name and the working
    directory the agent should operate in (typically ``data/repos``).
    Each call to :meth:`get_answer` runs
    ``opencode run --agent <agent> --format json --dir <working_dir>
    <query>`` and parses the newline-delimited JSON events emitted on
    stdout.

    Attributes:
        agent: Name of the OpenCode agent entry to invoke. Defaults to
            ``"docbot"``.
        working_dir: Directory the OpenCode agent runs in. The agent's
            tools (``read``, ``grep``, ``list``, ``bash``) operate
            relative to this directory, so it should be the root that
            contains the cloned repositories.
        opencode_bin: Name or path of the ``opencode`` executable.
            Defaults to ``"opencode"`` (resolved via ``PATH``).
    """

    def __init__(
        self,
        *,
        agent: str = "docbot",
        working_dir: Path | str = Path("data/repos"),
        opencode_bin: str = "opencode",
    ) -> None:
        """Initialize the LLM client.

        Args:
            agent: Name of the OpenCode agent entry to invoke. Defaults
                to ``"docbot"``.
            working_dir: Directory the OpenCode agent runs in. Defaults
                to ``data/repos``.
            opencode_bin: Name or path of the ``opencode`` executable.
                Defaults to ``"opencode"``.
        """
        self.agent: str = agent
        self.working_dir: Path = Path(working_dir)
        self.opencode_bin: str = opencode_bin

    async def get_answer(self, query: str) -> str:
        """Answer a user question by spawning an ``opencode`` subprocess.

        Runs ``opencode run --agent <agent> --format json --dir
        <working_dir> "<query>"`` and parses the newline-delimited JSON
        events written to stdout. The ``text`` field of every event
        whose ``type`` is ``"text"`` is concatenated (in emission order)
        and returned as the final answer.

        Args:
            query: The user's natural-language question.

        Returns:
            The concatenated ``text`` content from the agent's response
            events. Returns an empty string when the agent emits no
            ``text`` events.
        """
        query += "\n\n Keep your answer less than 2000 characters."
        _logger.debug(
            "Spawning opencode (agent=%s, dir=%s, query=%d chars)",
            self.agent,
            self.working_dir,
            len(query),
        )

        proc: asyncio.subprocess.Process = (
            await asyncio.create_subprocess_exec(
                self.opencode_bin,
                "run",
                "--agent",
                self.agent,
                "--format",
                "json",
                "--dir",
                str(self.working_dir),
                query,
                stdout=asyncio.subprocess.PIPE,
                stderr=asyncio.subprocess.PIPE,
            )
        )

        stdout_bytes, stderr_bytes = await proc.communicate()
        stdout: str = stdout_bytes.decode("utf-8", errors="replace")
        stderr: str = stderr_bytes.decode("utf-8", errors="replace")

        if proc.returncode != 0:
            _logger.error(
                "opencode exited with code %s; stderr: %s",
                proc.returncode,
                stderr.strip(),
            )

        answer: str = self._extract_text(stdout)
        _logger.debug(
            "opencode returned %d character(s) of text", len(answer)
        )
        return answer

    @staticmethod
    def _extract_text(stdout: str) -> str:
        """Extract and concatenate ``text`` events from OpenCode JSON output.

        Parses each non-empty line of ``stdout`` as a JSON object and
        collects the ``text`` field of every event whose ``type`` is
        ``"text"``. Malformed lines are skipped with a debug log so a
        stray diagnostic line does not abort the whole parse.

        Args:
            stdout: The raw newline-delimited JSON output from
                ``opencode run --format json``.

        Returns:
            The concatenated ``text`` content from all ``text`` events,
            in emission order. Returns an empty string when no ``text``
            events are present.
        """
        parts: list[str] = []
        for line in stdout.splitlines():
            stripped: str = line.strip()
            if not stripped:
                continue
            try:
                event: dict[str, object] = json.loads(stripped)
            except json.JSONDecodeError:
                _logger.debug(
                    "Skipping non-JSON opencode line: %s", stripped
                )
                continue
            if event.get("type") == "text":
                part = event.get("part", {})
                if isinstance(part, dict):
                    text: object = part.get("text", "")
                    if isinstance(text, str):
                        parts.append(text)
        return "".join(parts)
