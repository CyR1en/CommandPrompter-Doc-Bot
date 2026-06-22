"""Tests for :func:`bot.client._build_answer_embeds`."""

from __future__ import annotations

import pytest

from bot.client import (
    _EMBED_COLOR,
    _EMBED_DESCRIPTION_LIMIT,
    _build_answer_embeds,
)


# ---------------------------------------------------------------------------
# Trivial / single-page cases
# ---------------------------------------------------------------------------


def test_short_text_produces_single_embed() -> None:
    """Text that fits one page becomes a single embed with no footer."""
    text: str = "Short answer."
    embeds = _build_answer_embeds(text)
    assert len(embeds) == 1
    assert embeds[0].description == text
    # Single-page answers stay visually quiet: no footer.
    assert embeds[0].footer.text is None
    assert embeds[0].footer.icon_url is None


def test_empty_text_still_produces_one_embed() -> None:
    """An empty answer still yields one embed (the splitter returns
    ``[""]``), so callers always have at least one embed to send."""
    embeds = _build_answer_embeds("")
    assert len(embeds) == 1
    # ``split_message("")`` returns ``[""]`` so the description is the
    # empty string. Discord allows empty embed descriptions.
    assert embeds[0].description == ""


def test_text_at_exactly_the_limit_is_one_embed() -> None:
    """A string whose length equals the limit still fits in one page."""
    text: str = "x" * _EMBED_DESCRIPTION_LIMIT
    embeds = _build_answer_embeds(text)
    assert len(embeds) == 1
    assert embeds[0].description == text


# ---------------------------------------------------------------------------
# Multi-page cases
# ---------------------------------------------------------------------------


def test_long_text_splits_into_multiple_embeds() -> None:
    """Text longer than the limit is split across multiple embeds."""
    # Two full-limit pages + a trailing sentence forces ≥ 3 chunks.
    text: str = ("a" * _EMBED_DESCRIPTION_LIMIT) + "\n\n" + (
        "b" * _EMBED_DESCRIPTION_LIMIT
    ) + "\n\n" + "trailing"
    embeds = _build_answer_embeds(text)
    assert len(embeds) >= 2
    for embed in embeds:
        description: str | None = embed.description
        assert description is not None
        assert len(description) <= _EMBED_DESCRIPTION_LIMIT


def test_multi_page_embeds_carry_sequential_page_footers() -> None:
    """Every embed in a multi-page chain gets a ``"Page i/N"`` footer."""
    text: str = ("a" * _EMBED_DESCRIPTION_LIMIT) + "\n\n" + (
        "b" * _EMBED_DESCRIPTION_LIMIT
    )
    embeds = _build_answer_embeds(text)
    total: int = len(embeds)
    assert total >= 2
    for i, embed in enumerate(embeds, start=1):
        assert embed.footer.text == f"Page {i}/{total}"


def test_page_footers_cover_the_full_range() -> None:
    """The page indices start at 1 and end at ``total`` with no gaps."""
    text: str = "lorem ipsum " * 1000
    embeds = _build_answer_embeds(text)
    total: int = len(embeds)
    pages: list[int] = []
    for embed in embeds:
        footer_text: str | None = embed.footer.text
        assert footer_text is not None
        pages.append(int(footer_text.removeprefix("Page ").split("/")[0]))
    assert pages == list(range(1, total + 1))


# ---------------------------------------------------------------------------
# Embed metadata
# ---------------------------------------------------------------------------


def test_every_embed_uses_the_brand_color() -> None:
    """All embeds use the bot's brand color so the chain looks unified."""
    text: str = ("a" * _EMBED_DESCRIPTION_LIMIT) + "\n\n" + (
        "b" * _EMBED_DESCRIPTION_LIMIT
    )
    embeds = _build_answer_embeds(text)
    assert len(embeds) >= 2
    for embed in embeds:
        assert embed.colour == _EMBED_COLOR


def test_every_embed_has_a_timestamp() -> None:
    """Each embed is timestamped so the footer shows when the answer
    was generated (UTC)."""
    embeds = _build_answer_embeds("hello")
    assert len(embeds) == 1
    assert embeds[0].timestamp is not None


# ---------------------------------------------------------------------------
# Code-block preservation across embeds
# ---------------------------------------------------------------------------


def test_code_block_fence_is_preserved_across_pages() -> None:
    """A triple-backtick fence that straddles a page boundary is closed
    on the first page and reopened on the next so Discord's markdown
    renderer still sees valid fences."""
    # Build a text that comfortably exceeds the per-page limit and
    # contains a code block in the middle, so the natural split point
    # lands inside the block.
    prefix: str = ("p " * 1500).strip()  # ~3 000 chars
    suffix: str = ("s " * 1500).strip()  # ~3 000 chars
    code_body: str = "x = 1\n" * 500    # 3 500 chars of code
    inside: str = f"```python\n{code_body}```"  # ~3 512 chars
    text: str = f"{prefix}\n\n{inside}\n\n{suffix}"
    embeds = _build_answer_embeds(text)
    assert len(embeds) >= 2, (
        "test text is too short to force a multi-page split — "
        "increase the prefix/suffix length"
    )
    # Every embed's description must contain a balanced number of
    # triple-backtick fences (no embed is left mid-block).
    for embed in embeds:
        description: str | None = embed.description
        assert description is not None
        count: int = description.count("```")
        assert count % 2 == 0, (
            f"unbalanced fence in embed description: {description!r}"
        )


# ---------------------------------------------------------------------------
# Sanity / parametrised
# ---------------------------------------------------------------------------


@pytest.mark.parametrize("text", ["", "short", "x" * 100, "x" * 10000])
def test_every_embed_keeps_its_description_under_the_limit(text: str) -> None:
    """Across a range of input sizes, no embed's description ever
    exceeds the per-page limit."""
    embeds = _build_answer_embeds(text)
    assert len(embeds) >= 1
    for embed in embeds:
        description: str | None = embed.description
        assert description is not None
        assert len(description) <= _EMBED_DESCRIPTION_LIMIT
