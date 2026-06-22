"""Transform Discord-incompatible markdown into Discord-compatible.

Discord supports a subset of CommonMark / GFM. Some markdown constructs
that an LLM is likely to emit do not render correctly in Discord embeds
or messages, and would show up as raw pipe-delimited text or
untranslated rule characters. This module rewrites them into
Discord-friendly equivalents *before* the answer reaches the embed
builder, so the user never sees broken rendering.

What it handles
---------------

* **GFM tables** (a header row, a ``| --- | --- |`` separator, and one
  or more data rows) are converted into a bulleted list — each row
  becomes ``- **col1**: val1; **col2**: val2``.
* **Horizontal rules** (``---``, ``***``, ``___`` on their own line)
  are removed and replaced with a blank line so the paragraph break
  is preserved.
* **H4+ headers** (``####`` and deeper) are demoted to ``###`` since
  Discord only renders H1–H3.
* **Spacing** is normalized: trailing whitespace per line is stripped,
  runs of two or more blank lines collapse to a single blank line, and
  leading / trailing blank lines are trimmed from the whole text.

What it deliberately leaves alone
---------------------------------

* Anything inside triple-backtick code blocks. A literal ``---`` or
  ``| --- |`` inside a code fence is real code, not a divider, and
  must be passed through verbatim.
* Inline whitespace and indentation inside code blocks (would break
  the code).
* The actual content of the lines — only the structural shape is
  rewritten.
"""

from __future__ import annotations

import re
from collections.abc import Iterable

#: A triple-backtick (or longer) fence at the start of a line, with
#: optional leading whitespace. Captures the leading whitespace
#: (group 1) and the fence itself (group 2, length ≥ 3). The optional
#: language tag is not captured because the regex is only used to
#: detect fence lines for the code-block state machine.
_CODE_FENCE: re.Pattern[str] = re.compile(r"^(\s*)(```+).*$")

#: H4+ header — four or more ``#`` characters, then a space, then the
#: heading text. Group 1 is the run of hashes (used only to detect),
#: group 2 is the heading text.
_H4_PLUS_HEADER: re.Pattern[str] = re.compile(r"^(#{4,})\s+(.+?)\s*$")

#: A single cell of a markdown table separator row: optional leading
#: colon, one or more dashes, optional trailing colon. Used to
#: validate every cell of a candidate separator line.
_TABLE_SEPARATOR_CELL: re.Pattern[str] = re.compile(r"^:?-+:?$")


def _is_horizontal_rule(line: str) -> bool:
    """Return ``True`` if ``line`` is a CommonMark horizontal rule.

    A horizontal rule is a line containing three or more of the same
    character (``-``, ``*``, or ``_``), with spaces allowed between
    them. The whole-line match is stripped of whitespace before the
    character check, so ``" - - - "`` is an HR but ``"--"`` is not.
    """
    stripped: str = line.strip()
    if len(stripped) < 3:
        return False
    char: str = stripped[0]
    if char not in "-*_":
        return False
    # Stripping spaces lets ``" * * *"`` count as a ``*``-HR; the
    # remaining chars must all be the same ``char``.
    no_spaces: str = stripped.replace(" ", "")
    if len(no_spaces) < 3:
        return False
    return all(c == char for c in no_spaces)


def _is_table_separator(line: str) -> bool:
    """Return ``True`` if ``line`` is a markdown table separator row.

    Examples that count: ``| --- | --- |``, ``:---|:---:|---:``,
    ``| :-- | --: |``. Examples that do not: ``| hello | world |``
    (not dashes), an empty line, or a line with no ``|``.
    """
    stripped: str = line.strip()
    if not stripped:
        return False
    # Strip optional leading and trailing pipes so the line works
    # whether the table uses ``| x | y |`` or ``x | y``.
    inner: str = stripped.strip("|")
    if not inner:
        return False
    cells: list[str] = [c.strip() for c in inner.split("|")]
    for cell in cells:
        if not _TABLE_SEPARATOR_CELL.match(cell):
            return False
    return True


def _split_table_row(line: str) -> list[str]:
    """Parse a single ``| a | b |`` row into a list of cell strings.

    Leading and trailing pipes are optional. Each cell is stripped
    of surrounding whitespace. Empty cells are preserved as empty
    strings so the header / data column count stays consistent.
    """
    stripped: str = line.strip()
    if stripped.startswith("|"):
        stripped = stripped[1:]
    if stripped.endswith("|"):
        stripped = stripped[:-1]
    return [c.strip() for c in stripped.split("|")]


def _convert_table_to_bullets(
    header_line: str, data_rows: Iterable[str]
) -> list[str]:
    """Convert a markdown table to a list of bulleted lines.

    Each data row becomes ``- **col1**: val1; **col2**: val2`` (one
    bullet per row). If a row's column count does not match the
    header's (malformed table), the row is emitted as a raw bullet
    so the user still sees the data instead of nothing.
    """
    headers: list[str] = _split_table_row(header_line)
    bullets: list[str] = []
    for row_line in data_rows:
        if not row_line.strip():
            continue
        values: list[str] = _split_table_row(row_line)
        if len(values) != len(headers):
            # Fall back to a raw bullet so the row is not silently
            # dropped on a malformed table.
            bullets.append(f"- {row_line.strip()}")
            continue
        parts: list[str] = [f"**{h}**: {v}" for h, v in zip(headers, values)]
        bullets.append("- " + "; ".join(parts))
    return bullets


def _normalize_spacing(text: str) -> str:
    """Normalize whitespace: strip trailing spaces, collapse blank runs.

    Specifically:

    * Trailing whitespace is stripped from every line.
    * Runs of two or more blank lines collapse to a single blank line
      (so the document never has 3+ consecutive newlines).
    * Leading and trailing blank lines are stripped from the whole
      text so the embed does not start or end with whitespace.

    Indentation inside code blocks is preserved (the splitter only
    touches empty lines, never spaces within a non-empty line).
    """
    # 1. Strip trailing whitespace per line.
    lines: list[str] = [line.rstrip() for line in text.split("\n")]
    # 2. Collapse 2+ consecutive blank lines into 1.
    collapsed: list[str] = []
    blank_run: int = 0
    for line in lines:
        if line == "":
            blank_run += 1
            if blank_run <= 1:
                collapsed.append(line)
        else:
            blank_run = 0
            collapsed.append(line)
    # 3. Strip leading and trailing blank lines.
    while collapsed and collapsed[0] == "":
        collapsed.pop(0)
    while collapsed and collapsed[-1] == "":
        collapsed.pop()
    return "\n".join(collapsed)


def sanitize_markdown(text: str) -> str:
    """Rewrite Discord-incompatible markdown into Discord-compatible.

    Walks ``text`` line by line while tracking whether the cursor is
    inside a triple-backtick code block. Inside a code block, every
    line is passed through verbatim. Outside a code block, the
    following rewrites are applied:

    1. **Horizontal rules** (``---``, ``***``, ``___`` on their own
       line) are replaced with a blank line so the paragraph break
       is preserved.
    2. **GFM tables** — when a separator row is found with a
       pipe-separated line above it and one or more pipe-separated
       data rows below it, the whole table is rewritten as a
       bulleted list.
    3. **H4+ headers** (``####`` and deeper) are demoted to ``###``
       so Discord renders them.

    After the line pass, spacing is normalized: trailing whitespace
    is stripped, runs of 2+ blank lines collapse to 1, and leading /
    trailing blank lines are trimmed from the whole document.

    Args:
        text: The raw LLM answer. May be empty (returned unchanged).

    Returns:
        The same text with Discord-incompatible markdown rewritten
        and spacing normalized. Safe to feed straight into the embed
        builder.
    """
    if not text:
        return text

    lines: list[str] = text.split("\n")
    out: list[str] = []
    in_code_block: bool = False
    i: int = 0
    n: int = len(lines)

    while i < n:
        line: str = lines[i]

        # Code-block fence — toggle state and pass through. The fence
        # itself must be preserved verbatim (with its optional
        # language tag) so the embed's markdown renderer can match
        # open/close pairs.
        if _CODE_FENCE.match(line):
            in_code_block = not in_code_block
            out.append(line)
            i += 1
            continue

        # Inside a code block, every line is verbatim.
        if in_code_block:
            out.append(line)
            i += 1
            continue

        # Horizontal rule → blank line (keeps the paragraph break).
        if _is_horizontal_rule(line):
            out.append("")
            i += 1
            continue

        # Table: separator on this line. The header was already
        # appended on the previous iteration (or a previous call), so
        # ``out[-1]`` is the candidate header. The data rows are the
        # consecutive pipe-separated lines that follow.
        if _is_table_separator(line) and out and not _is_table_separator(out[-1]):
            header_line: str = out[-1]
            data_rows: list[str] = []
            j: int = i + 1
            while j < n:
                candidate: str = lines[j]
                # A data row must start with ``|`` (or be a pipe
                # somewhere — we check for startswith("|") to be
                # strict and avoid eating unrelated content).
                if not candidate.lstrip().startswith("|"):
                    break
                # Stop at another rule or another separator
                # (malformed table — abort the conversion).
                if _is_horizontal_rule(candidate) or _is_table_separator(candidate):
                    break
                # Stop at a code fence (the table ends before the
                # code block starts).
                if _CODE_FENCE.match(candidate):
                    break
                data_rows.append(candidate)
                j += 1
            # Only treat as a table if at least one data row was
            # found; otherwise fall through and treat the separator
            # as a regular line (it will not match a rule because of
            # the pipe, so it passes through unchanged).
            if data_rows:
                out.pop()  # remove the header line
                out.extend(_convert_table_to_bullets(header_line, data_rows))
                i = j
                continue

        # H4+ header → demote to H3 so Discord renders it.
        m: re.Match[str] | None = _H4_PLUS_HEADER.match(line)
        if m:
            out.append(f"### {m.group(2)}")
            i += 1
            continue

        out.append(line)
        i += 1

    return _normalize_spacing("\n".join(out))
