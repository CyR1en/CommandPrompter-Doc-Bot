"""Tests for :mod:`bot.tasks`.

The :class:`~core.git_manager.RepositoryManager` is mocked so the tests
exercise the polling logic without touching the network, the
filesystem's ``.git`` machinery, or real documentation files. The
single-iteration coroutine :func:`poll_repositories` is tested directly,
and :func:`build_polling_task` is verified to return a properly
configured :class:`discord.ext.tasks.Loop` whose wrapped coroutine drives
the same logic.
"""

from __future__ import annotations

import asyncio
from pathlib import Path
from unittest.mock import AsyncMock, MagicMock, patch

import pytest
from discord.ext import tasks

from bot.tasks import (
    DEFAULT_REPOS_ROOT,
    build_polling_task,
    derive_repo_name,
    inject_github_token,
    poll_repositories,
)


@pytest.fixture(autouse=True)
def mock_subprocess_exec():
    """Mock asyncio.create_subprocess_exec to prevent real subprocess spawns."""
    mock_proc = MagicMock()
    mock_proc.communicate = AsyncMock(return_value=(b"success", b""))
    mock_proc.returncode = 0
    with patch("bot.tasks.asyncio.create_subprocess_exec", new_callable=AsyncMock) as mock:
        mock.return_value = mock_proc
        yield mock


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


@pytest.mark.asyncio
async def test_poll_repositories_pulls_all_repos(
    tmp_path: Path,
) -> None:
    """Every configured repository is cloned/pulled in order.

    The manager is invoked once per URL with the injected URL and the
    derived destination path.
    """
    repo_manager = MagicMock(name="repo_manager")
    repo_manager.clone_or_pull.side_effect = [True, False]

    urls = [
        "https://github.com/org/repo1.git",
        "https://github.com/org/repo2.git",
    ]
    repos_root = tmp_path / "repos"

    await poll_repositories(repo_manager, urls, repos_root)

    assert repo_manager.clone_or_pull.call_count == 2
    first_call = repo_manager.clone_or_pull.call_args_list[0]
    second_call = repo_manager.clone_or_pull.call_args_list[1]
    assert first_call.args == (urls[0], repos_root / "repo1")
    assert second_call.args == (urls[1], repos_root / "repo2")


@pytest.mark.asyncio
async def test_poll_repositories_empty_urls_does_nothing(
    tmp_path: Path,
) -> None:
    """An empty URL list is a no-op for the manager."""
    repo_manager = MagicMock(name="repo_manager")

    await poll_repositories(repo_manager, [], tmp_path / "repos")

    repo_manager.clone_or_pull.assert_not_called()


@pytest.mark.asyncio
async def test_poll_repositories_uses_default_repos_root_when_given(
    tmp_path: Path,
) -> None:
    """The ``repos_root`` is forwarded verbatim to clone/pull destinations."""
    repo_manager = MagicMock(name="repo_manager")
    repo_manager.clone_or_pull.return_value = False

    await poll_repositories(
        repo_manager,
        ["https://github.com/o/repo.git"],
        DEFAULT_REPOS_ROOT,
    )

    repo_manager.clone_or_pull.assert_called_once_with(
        "https://github.com/o/repo.git", DEFAULT_REPOS_ROOT / "repo"
    )


@pytest.mark.asyncio
async def test_poll_repositories_logs_changes(tmp_path: Path) -> None:
    """The polling loop does not raise when repositories change.

    With the compiler removed, the task only pulls; a changed repo is
    logged but no longer triggers a recompile.
    """
    repo_manager = MagicMock(name="repo_manager")
    repo_manager.clone_or_pull.return_value = True

    urls = ["https://github.com/o/a.git", "https://github.com/o/b.git"]
    repos_root = tmp_path / "repos"

    # Must not raise even when every repo reports changes.
    await poll_repositories(repo_manager, urls, repos_root)

    assert repo_manager.clone_or_pull.call_count == 2


def test_build_polling_task_returns_loop_with_configured_interval() -> None:
    """The returned object is a :class:`tasks.Loop` with the right interval."""
    repo_manager = MagicMock(name="repo_manager")

    loop = build_polling_task(
        repo_manager,
        ["https://github.com/o/repo.git"],
        poll_interval_minutes=15,
        repos_root=Path("/srv/repos"),
    )

    assert isinstance(loop, tasks.Loop)
    assert loop.minutes == 15
    assert not loop.is_running()


@pytest.mark.asyncio
async def test_build_polling_task_default_repos_root(
    tmp_path: Path,
) -> None:
    """The default ``repos_root`` is :data:`DEFAULT_REPOS_ROOT`.

    The default is exercised indirectly: the wrapped coroutine is
    invoked and the destination path is checked against the default.
    """
    repo_manager = MagicMock(name="repo_manager")
    repo_manager.clone_or_pull.return_value = False

    loop = build_polling_task(
        repo_manager,
        ["https://github.com/o/repo.git"],
        poll_interval_minutes=5,
    )

    await loop.coro()

    repo_manager.clone_or_pull.assert_called_once_with(
        "https://github.com/o/repo.git", DEFAULT_REPOS_ROOT / "repo"
    )


@pytest.mark.asyncio
async def test_build_polling_task_coro_invokes_poll_logic(
    tmp_path: Path,
) -> None:
    """The wrapped coroutine drives :func:`poll_repositories` with the deps.

    Invoking ``loop.coro()`` directly runs a single iteration without
    scheduling the loop, which lets us assert the wiring without a
    running Discord client.
    """
    repo_manager = MagicMock(name="repo_manager")
    repo_manager.clone_or_pull.return_value = True

    repos_root = tmp_path / "repos"

    loop = build_polling_task(
        repo_manager,
        ["https://github.com/o/r.git"],
        poll_interval_minutes=10,
        repos_root=repos_root,
    )

    await loop.coro()

    repo_manager.clone_or_pull.assert_called_once_with(
        "https://github.com/o/r.git", repos_root / "r"
    )


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
        ("  https://github.com/org/repo.git  ", "tok", "https://oauth2:tok@github.com/org/repo.git"),
    ],
)
def test_inject_github_token(url: str, token: str | None, expected: str) -> None:
    """GitHub token is injected only for HTTPS GitHub URLs when token is provided."""
    assert inject_github_token(url, token) == expected


@pytest.mark.asyncio
async def test_poll_repositories_injects_github_token(tmp_path: Path) -> None:
    """When a GitHub token is provided, it is injected into matching URLs before cloning/pulling."""
    repo_manager = MagicMock(name="repo_manager")
    repo_manager.clone_or_pull.return_value = False

    urls = [
        "https://github.com/org/repo1.git",
        "https://gitlab.com/org/repo2.git",
    ]
    repos_root = tmp_path / "repos"

    await poll_repositories(
        repo_manager, urls, repos_root,
        token="secret-token",
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


@pytest.mark.asyncio
async def test_build_polling_task_passes_token(tmp_path: Path) -> None:
    """The polling task passes the configured token to poll_repositories."""
    repo_manager = MagicMock(name="repo_manager")
    repo_manager.clone_or_pull.return_value = False

    loop = build_polling_task(
        repo_manager,
        ["https://github.com/org/repo.git"],
        poll_interval_minutes=5,
        repos_root=tmp_path / "repos",
        token="another-token",
    )

    await loop.coro()

    repo_manager.clone_or_pull.assert_called_once_with(
        "https://oauth2:another-token@github.com/org/repo.git",
        (tmp_path / "repos") / "repo",
    )


@pytest.mark.asyncio
async def test_poll_repositories_spawns_opencode_on_changes(
    tmp_path: Path,
    mock_subprocess_exec: MagicMock,
) -> None:
    """When clone_or_pull returns True, opencode is spawned to generate AGENT.md."""
    repo_manager = MagicMock(name="repo_manager")
    repo_manager.clone_or_pull.return_value = True

    urls = ["https://github.com/org/repo.git"]
    repos_root = tmp_path / "repos"

    await poll_repositories(repo_manager, urls, repos_root)

    dest = repos_root / "repo"
    mock_subprocess_exec.assert_called_once_with(
        "opencode",
        "run",
        "--dangerously-skip-permissions",
        "--dir",
        str(dest),
        """
                    Analyze this repository and generate an overview of its structure, purpose, and key files.
                    The main goal of this file is to help the docbot agent quickly understand the repository's contents and how to navigate it when answering user questions.

                    <important>
                    Follow this format:

                    # Repository Overview
                    
                    # Source Tree

                    # Key Components

                    # Navigation Tips
                    </important>
                    
                    Do NOT include any information that cannot be directly observed from the repository's files and structure.
                    DO NOT include any information that is not directly supported by the repository's content. 
                    Do NOT attempt to infer or guess any information that is not explicitly stated in the repository.
                    If you don't see it in the repo, don't include it in the overview.

                    Save this overview to a file named AGENT.md in the root of the repository. Overwrite the file if it already exists.
                    Try keeping this file as short as possible while still providing a useful overview, ideally under 250 lines.
                    """,
        stdout=asyncio.subprocess.PIPE,
        stderr=asyncio.subprocess.PIPE,
    )


@pytest.mark.asyncio
async def test_poll_repositories_does_not_spawn_opencode_on_no_changes(
    tmp_path: Path,
    mock_subprocess_exec: MagicMock,
) -> None:
    """When clone_or_pull returns False, opencode is not spawned."""
    repo_manager = MagicMock(name="repo_manager")
    repo_manager.clone_or_pull.return_value = False

    urls = ["https://github.com/org/repo.git"]
    repos_root = tmp_path / "repos"

    await poll_repositories(repo_manager, urls, repos_root)

    mock_subprocess_exec.assert_not_called()


@pytest.mark.asyncio
async def test_poll_repositories_logs_opencode_failure(
    tmp_path: Path,
    mock_subprocess_exec: MagicMock,
) -> None:
    """When opencode fails, the failure is logged and no exception is raised."""
    repo_manager = MagicMock(name="repo_manager")
    repo_manager.clone_or_pull.return_value = True

    # Set up the mock process to return exit code 1
    mock_proc = MagicMock()
    mock_proc.communicate = AsyncMock(return_value=(b"", b"some error"))
    mock_proc.returncode = 1
    mock_subprocess_exec.return_value = mock_proc

    urls = ["https://github.com/org/repo.git"]
    repos_root = tmp_path / "repos"

    # Should not raise an exception
    await poll_repositories(repo_manager, urls, repos_root)

    dest = repos_root / "repo"
    mock_subprocess_exec.assert_called_once_with(
        "opencode",
        "run",
        "--dangerously-skip-permissions",
        "--dir",
        str(dest),
        """
                    Analyze this repository and generate an overview of its structure, purpose, and key files.
                    The main goal of this file is to help the docbot agent quickly understand the repository's contents and how to navigate it when answering user questions.

                    <important>
                    Follow this format:

                    # Repository Overview
                    
                    # Source Tree

                    # Key Components

                    # Navigation Tips
                    </important>
                    
                    Do NOT include any information that cannot be directly observed from the repository's files and structure.
                    DO NOT include any information that is not directly supported by the repository's content. 
                    Do NOT attempt to infer or guess any information that is not explicitly stated in the repository.
                    If you don't see it in the repo, don't include it in the overview.

                    Save this overview to a file named AGENT.md in the root of the repository. Overwrite the file if it already exists.
                    Try keeping this file as short as possible while still providing a useful overview, ideally under 250 lines.
                    """,
        stdout=asyncio.subprocess.PIPE,
        stderr=asyncio.subprocess.PIPE,
    )


@pytest.mark.asyncio
async def test_poll_repositories_handles_subprocess_exception(
    tmp_path: Path,
    mock_subprocess_exec: MagicMock,
) -> None:
    """When create_subprocess_exec raises an exception, it is handled and not reraised."""
    repo_manager = MagicMock(name="repo_manager")
    repo_manager.clone_or_pull.return_value = True

    mock_subprocess_exec.side_effect = FileNotFoundError("opencode not found")

    urls = ["https://github.com/org/repo.git"]
    repos_root = tmp_path / "repos"

    # Should not raise an exception
    await poll_repositories(repo_manager, urls, repos_root)
