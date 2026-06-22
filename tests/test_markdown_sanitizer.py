"""Tests for :mod:`core.markdown_sanitizer`."""

from __future__ import annotations

import pytest

from core.markdown_sanitizer import (
    _is_horizontal_rule,
    _is_table_separator,
    _normalize_spacing,
    sanitize_markdown,
)


# ---------------------------------------------------------------------------
# Horizontal rule detection
# ---------------------------------------------------------------------------


@pytest.mark.parametrize("line", [
    "---",
    "***",
    "___",
    " - - - ",
    "* * *",
    "_ _ _",
    "---------------",
    "***************",
    "_______________",
])
def test_horizontal_rule_variants_detected(line: str) -> None:
    """All three HR characters and spacing variants are detected."""
    assert _is_horizontal_rule(line) is True


@pytest.mark.parametrize("line", [
    "--",  # too short
    "**",  # too short
    "__",  # too short
    "-",   # too short
    "a--",  # mixed characters
    "-*-",  # mixed characters
    "===",  # not an HR char
    "text",  # not a rule at all
    "",     # empty
    "  ",   # whitespace only
])
def test_non_hr_lines_rejected(line: str) -> None:
    """Lines that look superficially similar to HRs are not flagged."""
    assert _is_horizontal_rule(line) is False


# ---------------------------------------------------------------------------
# Table separator detection
# ---------------------------------------------------------------------------


@pytest.mark.parametrize("line", [
    "| --- | --- |",
    ":---|:---:|---:",
    "|----|----|",
    "| :-- | --: |",
    "------|------",  # no leading pipe
    "----",           # minimal (1 cell)
])
def test_table_separator_variants_detected(line: str) -> None:
    """Separator rows with and without alignment markers are detected."""
    assert _is_table_separator(line) is True


@pytest.mark.parametrize("line", [
    "hello world",
    "| hello | world |",  # cells are not dashes
    "|----",              # single cell with no closing pipe is OK,
                          # but a paragraph above would be misclassified;
                          # we only test the function in isolation
    "",
    "   ",
])
def test_non_separator_lines_rejected(line: str) -> None:
    """Non-separator lines are not flagged (so they pass through)."""
    if line == "|----":
        # Single-cell separator is technically valid; skip this case.
        pytest.skip("valid single-cell separator")
    assert _is_table_separator(line) is False


# ---------------------------------------------------------------------------
# Table → bulleted list transformation
# ---------------------------------------------------------------------------


def test_simple_table_converted_to_bulleted_list() -> None:
    """A 2-column table becomes a list of single-bullet rows."""
    text: str = (
        "Header\n"
        "| col1 | col2 |\n"
        "| --- | --- |\n"
        "| a | b |\n"
        "| c | d |\n"
    )
    out: str = sanitize_markdown(text)
    assert "| " not in out
    assert "**col1**: a; **col2**: b" in out
    assert "**col1**: c; **col2**: d" in out


def test_table_with_alignment_markers_works() -> None:
    """Alignment markers in the separator row do not break conversion."""
    text: str = (
        "| name | age |\n"
        "| :--- | ---: |\n"
        "| Alice | 30 |\n"
        "| Bob | 25 |\n"
    )
    out: str = sanitize_markdown(text)
    assert ":---" not in out
    assert "**name**: Alice; **age**: 30" in out


def test_table_without_trailing_pipes_works() -> None:
    """Tables written without the trailing ``|`` are also converted."""
    text: str = (
        "| a | b\n"
        "| --- | ---\n"
        "| 1 | 2\n"
    )
    out: str = sanitize_markdown(text)
    assert "**a**: 1; **b**: 2" in out


def test_table_with_three_columns_works() -> None:
    """3-column tables are converted (multi-column header support)."""
    text: str = (
        "| x | y | z |\n"
        "| --- | --- | --- |\n"
        "| 1 | 2 | 3 |\n"
    )
    out: str = sanitize_markdown(text)
    assert "**x**: 1; **y**: 2; **z**: 3" in out


def test_multiple_tables_in_one_text_all_converted() -> None:
    """Two tables in the same answer are both converted independently."""
    text: str = (
        "First:\n"
        "| a | b |\n"
        "| --- | --- |\n"
        "| 1 | 2 |\n"
        "\n"
        "Second:\n"
        "| x | y |\n"
        "| --- | --- |\n"
        "| 9 | 8 |\n"
    )
    out: str = sanitize_markdown(text)
    assert "**a**: 1; **b**: 2" in out
    assert "**x**: 9; **y**: 8" in out


def test_malformed_table_falls_back_gracefully() -> None:
    """A table where the data row column count does not match the
    header does not crash — the row is emitted as a raw bullet so
    the data is still visible."""
    text: str = (
        "| a | b | c |\n"
        "| --- | --- | --- |\n"
        "| 1 | 2 |\n"  # missing 3rd column
    )
    out: str = sanitize_markdown(text)
    # The malformed data row survives as a raw bullet, not a silent
    # drop.
    assert "1 | 2" in out


# ---------------------------------------------------------------------------
# Horizontal rule transformation
# ---------------------------------------------------------------------------


@pytest.mark.parametrize("hr", ["---", "***", "___"])
def test_horizontal_rule_replaced_with_blank_line(hr: str) -> None:
    """All three HR characters are removed (replaced with a blank
    line so the surrounding paragraph break is preserved)."""
    text: str = f"Para 1\n\n{hr}\n\nPara 2"
    out: str = sanitize_markdown(text)
    assert hr not in out
    assert "Para 1\n\nPara 2" == out


def test_short_dashes_not_treated_as_hr() -> None:
    """Two dashes (``--``) are too short to be a HR and are preserved."""
    text: str = "A\n\n--\n\nB"
    out: str = sanitize_markdown(text)
    assert "--" in out


# ---------------------------------------------------------------------------
# Header demotion
# ---------------------------------------------------------------------------


@pytest.mark.parametrize("level", [4, 5, 6])
def test_h4_and_deeper_demoted_to_h3(level: int) -> None:
    """H4+ headers are demoted to H3 (Discord only renders H1–H3)."""
    text: str = f"{'#' * level} Deep"
    out: str = sanitize_markdown(text)
    assert out == "### Deep"
    # The original long-run of hashes is gone.
    assert ("#" * level) not in out


@pytest.mark.parametrize("line,expected", [
    ("# H1", "# H1"),
    ("## H2", "## H2"),
    ("### H3", "### H3"),
])
def test_h1_h2_h3_unchanged(line: str, expected: str) -> None:
    """H1, H2, H3 are not demoted (they render correctly in Discord)."""
    out: str = sanitize_markdown(line)
    assert out == expected


# ---------------------------------------------------------------------------
# Code block preservation
# ---------------------------------------------------------------------------


def test_horizontal_rule_inside_code_block_preserved() -> None:
    """A ``---`` inside a triple-backtick block is real code, not a
    divider, and must be left verbatim."""
    text: str = (
        "Para\n\n"
        "```python\n"
        "x = 1\n"
        "---\n"
        "y = 2\n"
        "```\n\n"
        "After"
    )
    out: str = sanitize_markdown(text)
    assert "```python\nx = 1\n---\ny = 2\n```" in out


def test_table_inside_code_block_preserved() -> None:
    """A table-shaped snippet inside a code block is not converted."""
    text: str = (
        "Para\n\n"
        "```\n"
        "| --- | --- |\n"
        "| 1 | 2 |\n"
        "```\n\n"
        "After"
    )
    out: str = sanitize_markdown(text)
    assert "| --- | --- |" in out
    assert "| 1 | 2 |" in out


def test_h4_inside_code_block_preserved() -> None:
    """An H4-shaped line inside a code block is not demoted."""
    text: str = (
        "```\n"
        "#### not a real header\n"
        "```"
    )
    out: str = sanitize_markdown(text)
    assert "#### not a real header" in out


def test_mixed_content_around_code_block() -> None:
    """Outside a code block, transformations apply normally; inside,
    they do not — even when both regions contain the same tokens."""
    text: str = (
        "Before\n\n"
        "---\n"
        "```\n"
        "--- kept inside code\n"
        "```\n"
        "---\n\n"
        "After"
    )
    out: str = sanitize_markdown(text)
    # Only the two outer rules are removed; the one inside the code
    # block survives.
    assert out.count("---") == 1
    assert "```\n--- kept inside code\n```" in out


# ---------------------------------------------------------------------------
# Spacing normalization
# ---------------------------------------------------------------------------


def test_trailing_whitespace_per_line_stripped() -> None:
    """Trailing spaces and tabs on each line are removed."""
    text: str = "hello   \nworld\t\nfoo \t \n"
    out: str = sanitize_markdown(text)
    assert out == "hello\nworld\nfoo"


def test_multiple_blank_lines_collapsed_to_single() -> None:
    """3+ consecutive newlines collapse to 2 (one blank line)."""
    text: str = "a\n\n\n\n\nb"
    out: str = sanitize_markdown(text)
    assert out == "a\n\nb"


def test_two_blank_lines_collapse_to_one() -> None:
    """2 consecutive newlines (already one blank line) stay as-is."""
    text: str = "a\n\nb"
    out: str = sanitize_markdown(text)
    assert out == "a\n\nb"


def test_leading_blank_lines_stripped() -> None:
    """Blank lines at the very start of the text are removed."""
    text: str = "\n\n\nhello\n"
    out: str = sanitize_markdown(text)
    assert out == "hello"


def test_trailing_blank_lines_stripped() -> None:
    """Blank lines at the very end of the text are removed."""
    text: str = "hello\n\n\n"
    out: str = sanitize_markdown(text)
    assert out == "hello"


def test_indentation_inside_code_preserved() -> None:
    """Leading spaces inside a line are preserved (would break code)."""
    text: str = "```python\n    if x:\n        pass\n```"
    out: str = sanitize_markdown(text)
    assert "    if x:" in out
    assert "        pass" in out


# ---------------------------------------------------------------------------
# Edge cases
# ---------------------------------------------------------------------------


def test_empty_input_returns_empty() -> None:
    """An empty input is returned unchanged."""
    assert sanitize_markdown("") == ""


def test_whitespace_only_input_returns_empty() -> None:
    """An input that is only whitespace returns an empty string."""
    assert sanitize_markdown("   \n\n  \n") == ""


def test_text_without_unsupported_markdown_unchanged() -> None:
    """A clean answer (no tables, no HRs, no H4+, no extra spacing)
    is returned as a no-op."""
    text: str = (
        "Hello world.\n\n"
        "This is **bold** and *italic*.\n\n"
        "## A header\n\n"
        "- item 1\n- item 2\n\n"
        "```python\nx = 1\n```\n\n"
        "Done."
    )
    out: str = sanitize_markdown(text)
    assert out == text


def test_sanitizer_is_idempotent() -> None:
    """Running the sanitizer twice produces the same output as once."""
    samples: list[str] = [
        "| a | b |\n| --- | --- |\n| 1 | 2 |",
        "A\n\n---\n\nB",
        "#### Deep",
        "```\n---\n```",
        "a\n\n\n\nb",
    ]
    for text in samples:
        once: str = sanitize_markdown(text)
        twice: str = sanitize_markdown(once)
        assert once == twice, (
            f"not idempotent: {text!r}\n  once: {once!r}\n  twice: {twice!r}"
        )


def test_unicode_text_handled() -> None:
    """The sanitizer is byte-safe on multi-byte characters."""
    text: str = "Hello \u2728 world\n\n---\n\n\u4e2d\u6587 test"
    out: str = sanitize_markdown(text)
    assert "\u2728" in out
    assert "\u4e2d\u6587" in out
    assert "---" not in out


# ---------------------------------------------------------------------------
# End-to-end: a realistic LLM-style answer
# ---------------------------------------------------------------------------


def test_realistic_llm_answer_fully_sanitized() -> None:
    """A realistic mixed answer — table, HR, H4, code block, extra
    blank lines, trailing whitespace — is sanitized end-to-end."""
    text: str = (
        "Here's a comparison of the three commands:\n"
        "\n"
        "| command | purpose | example |\n"
        "| --- | --- | --- |\n"
        "| /setspawn | set the spawn point | /setspawn home |\n"
        "| /home | teleport home | /home |\n"
        "\n"
        "#### Notes  \n"
        "The /setspawn command requires OP.\n"
        "\n"
        "```java\n"
        "// code with a marker inside, must be preserved verbatim\n"
        "@EventHandler\n"
        "public void onEnable() {}\n"
        "```\n"
        "\n"
        "----\n"
        "Section divider that should go away.\n"
        "\n"
        "\n"
        "\n"
        "Final paragraph.   \n"
    )
    out: str = sanitize_markdown(text)
    # Table converted.
    assert "**command**: /setspawn; **purpose**: set the spawn point" in out
    assert "**command**: /home; **purpose**: teleport home" in out
    # H4 demoted.
    assert "### Notes" in out
    # Code block preserved.
    assert "public void onEnable()" in out
    # HR removed — there is no `----` left in the output.
    assert "----" not in out
    # The code-block marker comment survives.
    assert "must be preserved verbatim" in out
    # Trailing whitespace stripped.
    assert not out.endswith("  \n")
    assert not any(line != line.rstrip() for line in out.split("\n"))
