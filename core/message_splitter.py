"""Split long text into chunks that fit Discord's per-message limit.

Discord rejects messages longer than 2000 characters with HTTP 400. The
bot's answers are produced by an LLM and can easily exceed that limit on
verbose responses, so the bot splits oversized answers into a chain of
smaller messages before sending.

The split is **content-aware** rather than a hard character cut: it
prefers paragraph breaks (``\\n\\n``), then line breaks (``\\n``), then
word boundaries (space), so the resulting chunks usually end at a
natural reading boundary. Triple-backtick code blocks are preserved
across splits — if a chosen split point would land inside an open
```` ``` ```` block, the block is closed at the end of the current chunk
and reopened at the start of the next chunk, so Discord's markdown
renderer still sees valid fences.
"""

from __future__ import annotations

#: Discord's hard per-message limit for non-nitro guild messages.
DEFAULT_LIMIT: int = 2000

#: Split-separator priority. The first match wins; the separator is
#: included in the left chunk so the boundary is not duplicated.
_SPLIT_SEPARATORS: tuple[str, ...] = ("\n\n", "\n", " ")

#: String used to open / close a code block when we have to split inside
#: one. Triple backticks on their own line, no language tag.
_CODE_FENCE: str = "```"


def split_message(text: str, limit: int = DEFAULT_LIMIT) -> list[str]:
    """Split ``text`` into chunks of at most ``limit`` characters.

    Iteratively chops the input at the best split point within the first
    ``limit`` characters until the remainder fits. The split separator
    (space, newline, or blank line) stays attached to the *right* chunk,
    so the left chunk ends at a clean word / line / paragraph boundary
    and ``"".join(chunks)`` reproduces the original ``text`` exactly
    (modulo any close+reopen adjustments for code blocks). When a split
    lands inside an open triple-backtick code block, the block is
    closed with ``\\n``` at the end of the current chunk and reopened
    with ```\\n``` at the start of the next chunk so the markdown stays
    valid on both sides.

    Args:
        text: The text to split. May contain newlines, code blocks, and
            other markdown.
        limit: Maximum characters per chunk. Must be a positive integer.
            Defaults to :data:`DEFAULT_LIMIT` (Discord's per-message
            limit for non-nitro).

    Returns:
        A non-empty list of chunk strings. The original input is
        returned unchanged (wrapped in a single-element list) when it
        already fits. An empty input returns ``[""]`` so callers can
        always rely on ``chunks[0]`` being valid.

    Raises:
        ValueError: If ``limit`` is not a positive integer.
    """
    if limit <= 0:
        raise ValueError(f"limit must be a positive integer, got {limit!r}")

    if text == "":
        # Never return an empty list — callers iterate without bounds
        # checks, so ``[""]`` keeps the contract simple.
        return [""]

    if len(text) <= limit:
        return [text]

    chunks: list[str] = []
    remaining: str = text
    while len(remaining) > limit:
        split_at: int = _find_split_point(remaining, limit)
        # The separator is left attached to the right chunk (e.g. the
        # trailing space or newline). The left chunk ends at a clean
        # word/line/paragraph boundary; the right chunk starts with the
        # separator, which is harmless for Discord rendering and
        # guarantees ``"".join(chunks) == text`` (modulo any
        # close+reopen adjustments made below).
        chunk: str = remaining[:split_at]
        remaining = remaining[split_at:]

        if chunk and _ends_inside_code_block(chunk):
            # Close the open block in the left chunk and reopen it in
            # the right chunk so both sides render correctly.
            chunk = f"{chunk}\n{_CODE_FENCE}"
            remaining = f"{_CODE_FENCE}\n{remaining}"

        chunks.append(chunk)

    if remaining:
        chunks.append(remaining)

    return chunks


def _find_split_point(text: str, limit: int) -> int:
    """Return the index of the best split separator in ``text[:limit]``.

    Scans :data:`_SPLIT_SEPARATORS` in priority order — the *first*
    separator that is present in the window wins, because the
    priority encodes "which boundary is the cleanest". Within that
    priority, the *rightmost* occurrence is used so the left chunk is
    as large as possible (and we produce the fewest total chunks).
    The returned index points at the separator itself, not after it:
    the caller slices ``text[:i]`` for the left chunk and ``text[i:]``
    for the right chunk, so the separator lives in the right chunk.

    Falls back to ``limit`` (a hard cut at the boundary) when no
    separator is found in ``text[:limit]``.

    Args:
        text: The text being split.
        limit: Hard upper bound on the returned index.

    Returns:
        An integer in ``(0, limit]`` that is safe to slice with
        ``text[:i]`` and ``text[i:]``.
    """
    for sep in _SPLIT_SEPARATORS:
        # ``rfind(sep, 0, limit)`` returns -1 when not found, or the
        # rightmost occurrence in the window. We skip a separator at
        # position 0 (it would produce an empty left chunk and loop
        # forever) and let a later priority or the hard cut handle it.
        idx: int = text.rfind(sep, 0, limit)
        if idx > 0:
            return idx
    return limit


def _ends_inside_code_block(text: str) -> bool:
    """Return ``True`` if ``text`` ends inside an unclosed code block.

    Walks ``text`` counting triple-backtick fences; an odd count means
    the most recent fence has not been closed. Inline single backticks
    are ignored. The optional language tag on a fence (e.g.
    ```` ```python ````) is skipped over so it is not mis-counted as a
    separate fence on the next line.

    Args:
        text: The text to inspect.

    Returns:
        ``True`` if the most recent ```` ``` ```` fence in ``text`` has
        no matching closer, ``False`` otherwise.
    """
    count: int = 0
    i: int = 0
    n: int = len(text)
    while i <= n - 3:
        if text[i : i + 3] == _CODE_FENCE:
            count += 1
            i += 3
            # Skip the optional language tag on the same line so we
            # don't count backticks inside it.
            while i < n and text[i] != "\n":
                i += 1
        else:
            i += 1
    return count % 2 == 1
