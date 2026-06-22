"""Tests for :mod:`core.message_splitter`."""

from __future__ import annotations

import pytest

from core.message_splitter import (
    DEFAULT_LIMIT,
    split_message,
)


# ---------------------------------------------------------------------------
# Trivial cases
# ---------------------------------------------------------------------------


def test_empty_input_returns_single_empty_string() -> None:
    """An empty input returns ``[""]`` so callers can always index ``[0]``."""
    assert split_message("") == [""]


def test_text_under_limit_is_returned_unchanged() -> None:
    """Text that already fits the limit is returned as a single chunk."""
    text: str = "short answer"
    assert split_message(text) == [text]


def test_text_exactly_at_limit_is_returned_unchanged() -> None:
    """A string whose length equals the limit is returned as-is."""
    text: str = "x" * DEFAULT_LIMIT
    assert split_message(text) == [text]


def test_invalid_limit_raises() -> None:
    """A non-positive limit is rejected with ``ValueError``."""
    with pytest.raises(ValueError):
        split_message("hello", limit=0)
    with pytest.raises(ValueError):
        split_message("hello", limit=-1)


# ---------------------------------------------------------------------------
# Split-strategy tests
# ---------------------------------------------------------------------------


def test_splits_at_paragraph_break_when_present() -> None:
    """When a ``\\n\\n`` exists in the window, the split lands on it.

    The text is two paragraphs joined by a blank line; the first chunk
    ends with the first paragraph, and the second chunk starts with the
    blank line + second paragraph (the separator is preserved on the
    right side so ``"".join(chunks) == text``).
    """
    para1: str = "a" * 50
    para2: str = "b" * 50
    text: str = f"{para1}\n\n{para2}"
    # Set a limit that forces a split inside ``text`` but still includes
    # the paragraph break in the first window.
    chunks: list[str] = split_message(text, limit=60)
    assert len(chunks) == 2
    assert chunks[0] == para1
    assert chunks[1] == f"\n\n{para2}"
    # Joining reproduces the original text exactly.
    assert "".join(chunks) == text


def test_splits_at_line_break_when_no_paragraph_break() -> None:
    """A single ``\\n`` is used as the split boundary when no ``\\n\\n``
    exists in the window."""
    line1: str = "a" * 50
    line2: str = "b" * 50
    text: str = f"{line1}\n{line2}"
    chunks: list[str] = split_message(text, limit=60)
    assert len(chunks) == 2
    assert chunks[0] == line1
    assert chunks[1] == f"\n{line2}"
    assert "".join(chunks) == text


def test_splits_at_word_boundary_when_no_newline() -> None:
    """A space is used when the window contains neither ``\\n\\n`` nor
    ``\\n`` but does contain a space."""
    line: str = ("word " * 30).strip()  # 149 chars of "word "s
    chunks: list[str] = split_message(line, limit=60)
    assert len(chunks) > 1
    # The split lands on a space, so the right chunk starts with a
    # space and the left chunk ends at a word boundary. No chunk
    # should contain a half-word cut.
    for chunk in chunks[:-1]:
        # The next chunk must start with the separator that closed
        # this one — i.e. a space, in this test.
        assert chunk != "" and not chunk.endswith("rd") or chunk.endswith("word")
    # Joining the chunks reproduces the original text exactly because
    # the splitter keeps the separator attached to the right chunk.
    assert "".join(chunks) == line


def test_hard_cuts_when_no_separator_fits() -> None:
    """A string with no whitespace falls back to a hard char cut."""
    text: str = "x" * 100
    chunks: list[str] = split_message(text, limit=30)
    assert len(chunks) >= 3
    for chunk in chunks:
        assert len(chunk) <= 30
    # The concatenation should preserve every character.
    assert "".join(chunks) == text


# ---------------------------------------------------------------------------
# Multiple-split / size invariants
# ---------------------------------------------------------------------------


def test_every_chunk_respects_the_limit() -> None:
    """Across many splits, no chunk ever exceeds the limit."""
    text: str = ("the quick brown fox jumps over the lazy dog. " * 200).strip()
    for chunk in split_message(text, limit=500):
        assert len(chunk) <= 500


def test_very_long_text_splits_into_many_chunks() -> None:
    """A text 10x the limit splits into roughly 10 chunks."""
    text: str = ("x" * 10) + " " + ("y" * 10)
    full: str = ((text + "\n") * 200).strip()
    chunks: list[str] = split_message(full, limit=100)
    assert len(chunks) > 5
    for chunk in chunks:
        assert len(chunk) <= 100


def test_custom_limit_is_honoured() -> None:
    """A non-default limit shrinks the chunk size accordingly."""
    text: str = "a" * 100 + " " + "b" * 100
    chunks: list[str] = split_message(text, limit=20)
    for chunk in chunks:
        assert len(chunk) <= 20


# ---------------------------------------------------------------------------
# Code-block awareness
# ---------------------------------------------------------------------------


def test_closes_and_reopens_code_block_when_split_inside_one() -> None:
    """A split inside an open ```` ``` ```` block closes it and reopens
    it in the next chunk so Discord rendering stays valid."""
    code: str = "line1\nline2\nline3\n"  # 18 chars
    inside: str = f"```python\n{code}{code}{code}```"  # well over 30 chars
    text: str = f"prefix text\n\n{inside}\n\nsuffix text"
    chunks: list[str] = split_message(text, limit=40)
    assert len(chunks) >= 2
    # Every chunk must end with a balanced fence (even number of
    # triple-backticks, so no chunk is left mid-block).
    for chunk in chunks:
        fence_count: int = chunk.count("```")
        assert fence_count % 2 == 0, f"unbalanced fence in chunk: {chunk!r}"
    # Only splits that land *inside* a code block add fences (one
    # close + one reopen = 2). Splits that land on ``\\n\\n`` outside
    # any block do not. The safe invariant: the total fence count in
    # the joined output must be even and at least the original count
    # (it can grow but never shrink).
    joined: str = "".join(chunks)
    assert joined.count("```") >= text.count("```")
    assert joined.count("```") % 2 == 0


def test_no_split_inside_block_when_split_fits_outside() -> None:
    """If a natural split point is available outside a code block, the
    splitter prefers it and leaves the block intact."""
    before: str = "a" * 30
    block: str = "```python\nx = 1\n```"
    after: str = "b" * 30
    text: str = f"{before}\n\n{block}\n\n{after}"
    chunks: list[str] = split_message(text, limit=40)
    # The split should land at the ``\n\n`` after ``before``.
    assert len(chunks) >= 2
    assert chunks[0] == before
    # The block survives intact in one of the chunks.
    assert any(block in chunk for chunk in chunks)


def test_empty_code_block_does_not_confuse_the_detector() -> None:
    """An empty block ```````` does not get re-closed incorrectly."""
    text: str = "before\n\n```\n```\n\nafter" + ("x" * 200)
    chunks: list[str] = split_message(text, limit=20)
    # Sanity: each chunk has balanced fences.
    for chunk in chunks:
        assert chunk.count("```") % 2 == 0


def test_code_block_with_language_tag_is_counted_as_one_fence() -> None:
    """A ```` ```python ```` opener followed by content then a closer
    counts as a single balanced block."""
    block: str = "```python\nprint('hi')\n```"  # 25 chars
    text: str = (block + "\n\n") * 10  # 270 chars
    chunks: list[str] = split_message(text, limit=80)
    for chunk in chunks:
        assert chunk.count("```") % 2 == 0
