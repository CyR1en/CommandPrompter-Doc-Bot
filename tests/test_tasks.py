"""Tests for :mod:`bot.tasks`.

The :class:`~core.git_manager.RepositoryManager` is mocked so the tests
exercise the polling logic without touching the network, the
filesystem's ``.git`` machinery, or real documentation files. The
:class:`~core.opencode_client.OpencodeClient` is also mocked so the
AGENT.md-generation flow exercises the HTTP API path without
spinning up a real ``opencode serve`` subprocess.

The single-iteration coroutine :func:`poll_repositories` is tested
directly, and :func:`build_polling_task` is verified to return a
properly configured :class:`discord.ext.tasks.Loop` whose wrapped
coroutine drives the same logic.
"""

from __future__ import annotations

from contextlib import nullcontext
from pathlib import Path
from unittest.mock import AsyncMock, MagicMock, patch

import pytest
from discord.ext import tasks

from bot.tasks import (
    DEFAULT_REPOS_ROOT,
    _AGENT_MD_PROMPT_TEMPLATE,
    _generate_agent_md,
    build_polling_task,
    derive_repo_name,
    inject_github_token,
    poll_repositories,
)
from core.opencode_client import OpencodeClientError


# ---------------------------------------------------------------------------
# Fixtures
# ---------------------------------------------------------------------------


def _make_opencode_client(
    *,
    create_returns: str = "ses_test",
    prompt_returns: str = "ok",
    create_side_effect: BaseException | None = None,
    prompt_side_effect: BaseException | None = None,
    delete_side_effect: BaseException | None = None,
) -> MagicMock:
    """Build a mocked :class:`OpencodeClient` with sensible defaults.

    All three methods (``create_session``, ``prompt``,
    ``delete_session``) are :class:`AsyncMock`. Pass a side-effect to
    simulate a failure on any of them.

    Args:
        create_returns: Session id returned by ``create_session``.
        prompt_returns: Text returned by ``prompt``.
        create_side_effect: Exception raised by ``create_session`` if
            set.
        prompt_side_effect: Exception raised by ``prompt`` if set.
        delete_side_effect: Exception raised by ``delete_session`` if
            set.

    Returns:
        A :class:`MagicMock` whose ``create_session`` / ``prompt`` /
        ``delete_session`` are async mocks.
    """
    client = MagicMock(name="opencode_client")
    create = AsyncMock(return_value=create_returns)
    if create_side_effect is not None:
        create.side_effect = create_side_effect
    prompt = AsyncMock(return_value=prompt_returns)
    if prompt_side_effect is not None:
        prompt.side_effect = prompt_side_effect
    delete = AsyncMock()
    if delete_side_effect is not None:
        delete.side_effect = delete_side_effect
    client.create_session = create
    client.prompt = prompt
    client.delete_session = delete
    return client


@pytest.fixture(autouse=True)
def mock_opencode_client():
    """Provide a default mocked :class:`OpencodeClient` to all tests.

    Tests that need to inspect calls or override behaviour request the
    fixture explicitly as a parameter. The default mock is
    side-effect-free (all methods return successfully) so tests that
    don't care about it just get a no-op.
    """
    # The autouse fixture exists so tests can pass it explicitly; it
    # does not need to do anything globally because ``poll_repositories``
    # takes the client as a required keyword arg.
    yield _make_opencode_client()


# ---------------------------------------------------------------------------
# Pure helpers
# ---------------------------------------------------------------------------


@pytest.mark.parametrize(
    "url,expected",
    [
        ("https://github.com/org/repo.git", "repo"),
        ("https://github.com/org/repo", "repo"),
        ("https://github.com/org/Repo.GIT", "Repo"),
        ("https://github.com/org/repo/", "repo"),
        ("git@github.com:org/my-plugin.git", "my-plugin"),
        ("git@gitlab.com:team/sub/project.git", "project"),
        ("  https://github.com/org/whitespace.git  ", "whitespace"),
        ("https://github.com/org/repo.git#dev", "repo"),
        ("https://github.com/org/repo#main", "repo"),
        ("git@github.com:org/my-plugin.git#feature-branch", "my-plugin"),
    ],
)
def test_derive_repo_name(url: str, expected: str) -> None:
    """Repository names are derived from the URL's final path segment."""
    assert derive_repo_name(url) == expected


@pytest.mark.parametrize(
    "url,token,expected",
    [
        ("https://github.com/org/repo.git", "tok", "https://oauth2:tok@github.com/org/repo.git"),
        ("https://www.github.com/org/repo.git", "tok", "https://oauth2:tok@github.com/org/repo.git"),
        ("HTTPS://GITHUB.COM/org/repo.git", "tok", "https://oauth2:tok@github.com/org/repo.git"),
        ("HTTPS://WWW.GITHUB.COM/org/repo.git", "tok", "https://oauth2:tok@github.com/org/repo.git"),
        ("https://github.com/org/repo.git", None, "https://github.com/org/repo.git"),
        ("https://github.com/org/repo.git", "", "https://github.com/org/repo.git"),
        ("https://gitlab.com/org/repo.git", "tok", "https://gitlab.com/org/repo.git"),
        ("git@github.com:org/repo.git", "tok", "git@github.com:org/repo.git"),
        ("  https://github.com/org/whitespace.git  ", "tok", "https://oauth2:tok@github.com/org/whitespace.git"),
    ],
)
def test_inject_github_token(url: str, token: str | None, expected: str) -> None:
    """GitHub token is injected only for HTTPS GitHub URLs when token is provided."""
    assert inject_github_token(url, token) == expected


# ---------------------------------------------------------------------------
# poll_repositories
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_poll_repositories_pulls_all_repos(
    tmp_path: Path,
    mock_opencode_client: MagicMock,
) -> None:
    """Every configured repository is cloned/pulled in order."""
    repo_manager = MagicMock(name="repo_manager")
    repo_manager.clone_or_pull.side_effect = [True, False]

    urls = [
        "https://github.com/org/repo1.git",
        "https://github.com/org/repo2.git",
    ]
    repos_root = tmp_path / "repos"

    await poll_repositories(
        repo_manager,
        urls,
        repos_root,
        client=mock_opencode_client,
        provider_id="opencode",
        model_id="m",
    )

    assert repo_manager.clone_or_pull.call_count == 2
    first_call = repo_manager.clone_or_pull.call_args_list[0]
    second_call = repo_manager.clone_or_pull.call_args_list[1]
    assert first_call.args == (urls[0], repos_root / "repo1")
    assert second_call.args == (urls[1], repos_root / "repo2")


@pytest.mark.asyncio
async def test_poll_repositories_empty_urls_does_nothing(
    tmp_path: Path,
    mock_opencode_client: MagicMock,
) -> None:
    """An empty URL list is a no-op for the manager and the opencode client."""
    repo_manager = MagicMock(name="repo_manager")

    await poll_repositories(
        repo_manager,
        [],
        tmp_path / "repos",
        client=mock_opencode_client,
        provider_id="opencode",
        model_id="m",
    )

    repo_manager.clone_or_pull.assert_not_called()
    mock_opencode_client.create_session.assert_not_called()


@pytest.mark.asyncio
async def test_poll_repositories_uses_default_repos_root_when_given(
    mock_opencode_client: MagicMock,
) -> None:
    """The ``repos_root`` is forwarded verbatim to clone/pull destinations."""
    repo_manager = MagicMock(name="repo_manager")
    repo_manager.clone_or_pull.return_value = False

    await poll_repositories(
        repo_manager,
        ["https://github.com/o/repo.git"],
        DEFAULT_REPOS_ROOT,
        client=mock_opencode_client,
        provider_id="opencode",
        model_id="m",
    )

    repo_manager.clone_or_pull.assert_called_once_with(
        "https://github.com/o/repo.git", DEFAULT_REPOS_ROOT / "repo"
    )


@pytest.mark.asyncio
async def test_poll_repositories_injects_github_token(
    tmp_path: Path,
    mock_opencode_client: MagicMock,
) -> None:
    """When a GitHub token is provided, it is injected into matching URLs before cloning/pulling."""
    repo_manager = MagicMock(name="repo_manager")
    repo_manager.clone_or_pull.return_value = False

    urls = [
        "https://github.com/org/repo1.git",
        "https://gitlab.com/org/repo2.git",
    ]
    repos_root = tmp_path / "repos"

    await poll_repositories(
        repo_manager,
        urls,
        repos_root,
        token="secret-token",
        client=mock_opencode_client,
        provider_id="opencode",
        model_id="m",
    )

    assert repo_manager.clone_or_pull.call_count == 2
    first_call = repo_manager.clone_or_pull.call_args_list[0]
    second_call = repo_manager.clone_or_pull.call_args_list[1]

    assert first_call.args == (
        "https://oauth2:secret-token@github.com/org/repo1.git",
        repos_root / "repo1",
    )
    assert second_call.args == (
        "https://gitlab.com/org/repo2.git",
        repos_root / "repo2",
    )


# ---------------------------------------------------------------------------
# AGENT.md generation (HTTP API path)
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_poll_repositories_calls_opencode_on_changes(
    tmp_path: Path,
    mock_opencode_client: MagicMock,
) -> None:
    """When a repo changes, the opencode HTTP client is used to generate AGENT.md.

    Regression guard: the old implementation spawned
    ``opencode run`` as a subprocess. The new implementation goes
    through :class:`OpencodeClient` and the served HTTP API, using
    the same provider/model the bot is configured for.
    """
    repo_manager = MagicMock(name="repo_manager")
    repo_manager.clone_or_pull.return_value = True

    urls = ["https://github.com/org/repo.git"]
    repos_root = tmp_path / "repos"

    await poll_repositories(
        repo_manager,
        urls,
        repos_root,
        client=mock_opencode_client,
        provider_id="opencode",
        model_id="deepseek-v4-flash-free",
        variant="max",
    )

    dest: Path = repos_root / "repo"
    # create_session called with the configured provider/model and the
    # default agent (agent=None → omitted from the body).
    mock_opencode_client.create_session.assert_awaited_once_with(
        title=f"agent-md:{dest.name}",
        provider_id="opencode",
        model_id="deepseek-v4-flash-free",
    )
    # prompt called with the templated prompt text (absolute path
    # embedded so the agent knows where to write) and the variant.
    prompt_call = mock_opencode_client.prompt.call_args
    assert prompt_call.kwargs["session_id"] == "ses_test"
    assert prompt_call.kwargs["provider_id"] == "opencode"
    assert prompt_call.kwargs["model_id"] == "deepseek-v4-flash-free"
    assert prompt_call.kwargs["variant"] == "max"
    parts = prompt_call.kwargs["parts"]
    assert len(parts) == 1
    assert parts[0]["type"] == "text"
    assert str(dest) in parts[0]["text"]
    assert "AGENT.md" in parts[0]["text"]
    # Session best-effort-deleted.
    mock_opencode_client.delete_session.assert_awaited_once_with("ses_test")


@pytest.mark.asyncio
async def test_poll_repositories_skips_opencode_on_no_changes(
    tmp_path: Path,
    mock_opencode_client: MagicMock,
) -> None:
    """When no repo changed, the opencode client is not touched."""
    repo_manager = MagicMock(name="repo_manager")
    repo_manager.clone_or_pull.return_value = False

    urls = ["https://github.com/org/repo.git"]
    repos_root = tmp_path / "repos"

    await poll_repositories(
        repo_manager,
        urls,
        repos_root,
        client=mock_opencode_client,
        provider_id="opencode",
        model_id="m",
    )

    mock_opencode_client.create_session.assert_not_called()
    mock_opencode_client.prompt.assert_not_called()
    mock_opencode_client.delete_session.assert_not_called()


@pytest.mark.asyncio
async def test_poll_repositories_logs_and_continues_on_opencode_failure(
    tmp_path: Path,
    caplog: pytest.LogCaptureFixture,
) -> None:
    """A failure inside the opencode client is logged; polling does not raise.

    The ``OpencodeClientError`` is raised from
    ``OpencodeClient.create_session`` (the mock has ``side_effect``),
    the helper logs at ERROR level and returns ``False``, and the
    outer loop continues to the next repo.
    """
    import logging

    client = _make_opencode_client(
        create_side_effect=OpencodeClientError("boom")
    )
    repo_manager = MagicMock(name="repo_manager")
    repo_manager.clone_or_pull.side_effect = [True, False]

    urls = [
        "https://github.com/org/repo1.git",
        "https://github.com/org/repo2.git",
    ]
    repos_root = tmp_path / "repos"

    caplog.set_level(logging.ERROR, logger="bot.tasks")
    # Must not raise.
    await poll_repositories(
        repo_manager,
        urls,
        repos_root,
        client=client,
        provider_id="opencode",
        model_id="m",
    )

    # Both repos were polled; the first one failed AGENT.md generation
    # but the loop continued.
    assert repo_manager.clone_or_pull.call_count == 2
    # The failure was logged for the first repo.
    assert any(
        "Failed to generate AGENT.md" in record.message
        and "repo1" in record.message
        for record in caplog.records
    )


# ---------------------------------------------------------------------------
# _generate_agent_md (helper-level)
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_generate_agent_md_happy_path() -> None:
    """The helper creates, prompts, and deletes a session in order."""
    client = _make_opencode_client()
    repo_dir = Path("/data/repos/CommandPrompter")

    ok: bool = await _generate_agent_md(
        client,
        repo_dir=repo_dir,
        provider_id="opencode",
        model_id="m",
        variant="max",
    )

    assert ok is True
    client.create_session.assert_awaited_once()
    client.prompt.assert_awaited_once()
    client.delete_session.assert_awaited_once_with("ses_test")


@pytest.mark.asyncio
async def test_generate_agent_md_prompt_includes_absolute_repo_path() -> None:
    """The prompt text embeds the absolute repo path so the agent knows where to write."""
    client = _make_opencode_client()
    repo_dir = Path("/data/repos/CommandPrompter")

    await _generate_agent_md(
        client,
        repo_dir=repo_dir,
        provider_id="opencode",
        model_id="m",
    )

    text: str = client.prompt.call_args.kwargs["parts"][0]["text"]
    assert "/data/repos/CommandPrompter" in text
    assert "AGENT.md" in text
    # The template variables are all substituted (no literal
    # ``{repo_dir}`` left in the rendered prompt).
    assert "{repo_dir}" not in text


@pytest.mark.asyncio
async def test_generate_agent_md_prompt_allows_subagent_orchestration() -> None:
    """The prompt explicitly allows the agent to spawn subagents in parallel.

    Regression guard: the original subprocess-based prompt included
    "you will orchestrate two subagents to explore the repository in
    parallel" and the HTTP migration dropped it. This test pins the
    subagent instruction so it can't be accidentally removed again.
    """
    client = _make_opencode_client()

    await _generate_agent_md(
        client,
        repo_dir=Path("/r"),
        provider_id="opencode",
        model_id="m",
    )

    text: str = client.prompt.call_args.kwargs["parts"][0]["text"]
    assert "subagent" in text.lower()
    assert "parallel" in text.lower()


@pytest.mark.asyncio
async def test_generate_agent_md_returns_false_on_create_session_error() -> None:
    """A ``OpencodeClientError`` on create_session → False, session NOT deleted."""
    client = _make_opencode_client(
        create_side_effect=OpencodeClientError("create failed")
    )

    ok: bool = await _generate_agent_md(
        client,
        repo_dir=Path("/r"),
        provider_id="opencode",
        model_id="m",
    )

    assert ok is False
    client.prompt.assert_not_called()
    client.delete_session.assert_not_called()


@pytest.mark.asyncio
async def test_generate_agent_md_returns_false_on_prompt_error_but_still_deletes() -> None:
    """A ``OpencodeClientError`` on prompt → False, session still best-effort deleted."""
    client = _make_opencode_client(
        prompt_side_effect=OpencodeClientError("prompt failed")
    )

    ok: bool = await _generate_agent_md(
        client,
        repo_dir=Path("/r"),
        provider_id="opencode",
        model_id="m",
    )

    assert ok is False
    # Session was created so it must be deleted (the ``finally`` block).
    client.delete_session.assert_awaited_once_with("ses_test")


@pytest.mark.asyncio
async def test_generate_agent_md_swallows_delete_error() -> None:
    """A failure during ``delete_session`` does not propagate."""
    client = _make_opencode_client(
        delete_side_effect=OpencodeClientError("delete failed")
    )

    # Must not raise even though delete fails.
    ok: bool = await _generate_agent_md(
        client,
        repo_dir=Path("/r"),
        provider_id="opencode",
        model_id="m",
    )

    assert ok is True
    client.delete_session.assert_awaited_once_with("ses_test")


@pytest.mark.asyncio
async def test_generate_agent_md_returns_false_on_unexpected_exception() -> None:
    """A non-``OpencodeClientError`` exception is caught, logged, and returns False.

    The session is still best-effort-deleted (the ``finally`` block
    runs even for un-caught exceptions).
    """
    client = _make_opencode_client(
        prompt_side_effect=ValueError("unexpected")
    )

    ok: bool = await _generate_agent_md(
        client,
        repo_dir=Path("/r"),
        provider_id="opencode",
        model_id="m",
    )

    assert ok is False
    client.delete_session.assert_awaited_once_with("ses_test")


# ---------------------------------------------------------------------------
# build_polling_task
# ---------------------------------------------------------------------------


def test_build_polling_task_returns_loop_with_configured_interval() -> None:
    """The returned object is a :class:`tasks.Loop` with the right interval."""
    repo_manager = MagicMock(name="repo_manager")
    client = _make_opencode_client()

    loop = build_polling_task(
        repo_manager,
        ["https://github.com/o/repo.git"],
        poll_interval_minutes=15,
        repos_root=Path("/srv/repos"),
        client=client,
        provider_id="opencode",
        model_id="m",
    )

    assert isinstance(loop, tasks.Loop)
    assert loop.minutes == 15
    assert not loop.is_running()


@pytest.mark.asyncio
async def test_build_polling_task_default_repos_root(
    mock_opencode_client: MagicMock,
) -> None:
    """The default ``repos_root`` is :data:`DEFAULT_REPOS_ROOT`."""
    repo_manager = MagicMock(name="repo_manager")
    repo_manager.clone_or_pull.return_value = False

    loop = build_polling_task(
        repo_manager,
        ["https://github.com/o/repo.git"],
        poll_interval_minutes=5,
        client=mock_opencode_client,
        provider_id="opencode",
        model_id="m",
    )

    await loop.coro()

    repo_manager.clone_or_pull.assert_called_once_with(
        "https://github.com/o/repo.git", DEFAULT_REPOS_ROOT / "repo"
    )


@pytest.mark.asyncio
async def test_build_polling_task_coro_invokes_poll_logic(
    tmp_path: Path,
    mock_opencode_client: MagicMock,
) -> None:
    """The wrapped coroutine drives :func:`poll_repositories` with the deps."""
    repo_manager = MagicMock(name="repo_manager")
    repo_manager.clone_or_pull.return_value = True

    repos_root = tmp_path / "repos"

    loop = build_polling_task(
        repo_manager,
        ["https://github.com/o/r.git"],
        poll_interval_minutes=10,
        repos_root=repos_root,
        client=mock_opencode_client,
        provider_id="opencode",
        model_id="m",
        variant="high",
    )

    await loop.coro()

    repo_manager.clone_or_pull.assert_called_once_with(
        "https://github.com/o/r.git", repos_root / "r"
    )
    # The opencode client was called with the configured model/variant.
    mock_opencode_client.create_session.assert_awaited_once()
    prompt_kwargs = mock_opencode_client.prompt.call_args.kwargs
    assert prompt_kwargs["variant"] == "high"


@pytest.mark.asyncio
async def test_build_polling_task_passes_token(
    tmp_path: Path,
    mock_opencode_client: MagicMock,
) -> None:
    """The polling task passes the configured token to ``poll_repositories``."""
    repo_manager = MagicMock(name="repo_manager")
    repo_manager.clone_or_pull.return_value = False

    loop = build_polling_task(
        repo_manager,
        ["https://github.com/org/repo.git"],
        poll_interval_minutes=5,
        repos_root=tmp_path / "repos",
        token="another-token",
        client=mock_opencode_client,
        provider_id="opencode",
        model_id="m",
    )

    await loop.coro()

    repo_manager.clone_or_pull.assert_called_once_with(
        "https://oauth2:another-token@github.com/org/repo.git",
        (tmp_path / "repos") / "repo",
    )
