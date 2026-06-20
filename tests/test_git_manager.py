"""Tests for :mod:`core.git_manager` repository syncing logic.

The GitPython ``git.Repo`` interface is mocked so the tests never touch
the network or the local filesystem's ``.git`` machinery. Real
directories (created under ``tmp_path``) are used only to exercise the
"fresh vs. existing" detection in :meth:`RepositoryManager.clone_or_pull`.
"""

from __future__ import annotations

from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest

from core.git_manager import RepositoryManager

_REPO_URL = "https://github.com/example/repo.git"


@patch("core.git_manager.git.Repo")
def test_clone_when_dest_does_not_exist(
    mock_repo_cls: MagicMock,
    tmp_path: Path,
) -> None:
    """A missing destination triggers a fresh clone and returns ``True``."""
    dest = tmp_path / "repo"  # intentionally not created
    mock_repo_cls.clone_from.return_value = MagicMock()

    manager = RepositoryManager()
    result = manager.clone_or_pull(_REPO_URL, dest)

    assert result is True
    mock_repo_cls.clone_from.assert_called_once_with(_REPO_URL, dest)
    # The Repo constructor is only used to open existing clones.
    mock_repo_cls.assert_not_called()


@patch("core.git_manager.git.Repo")
def test_clone_when_dest_is_empty(
    mock_repo_cls: MagicMock,
    tmp_path: Path,
) -> None:
    """An existing but empty destination is treated as a fresh clone."""
    dest = tmp_path / "repo"
    dest.mkdir()  # exists, but contains nothing
    mock_repo_cls.clone_from.return_value = MagicMock()

    manager = RepositoryManager()
    result = manager.clone_or_pull(_REPO_URL, dest)

    assert result is True
    mock_repo_cls.clone_from.assert_called_once_with(_REPO_URL, dest)
    mock_repo_cls.assert_not_called()


@patch("core.git_manager.git.Repo")
def test_pull_returns_true_when_head_changes(
    mock_repo_cls: MagicMock,
    tmp_path: Path,
) -> None:
    """A pull that moves ``HEAD`` returns ``True``."""
    dest = tmp_path / "repo"
    dest.mkdir()
    (dest / "marker").write_text("x", encoding="utf-8")  # make non-empty

    mock_repo = MagicMock()
    mock_repo_cls.return_value = mock_repo
    mock_repo.head.commit.hexsha = "abc123"

    def _pull_side_effect(*_args: object, **_kwargs: object) -> list[object]:
        # Simulate the remote delivering new commits.
        mock_repo.head.commit.hexsha = "def456"
        return []

    mock_repo.remotes.origin.pull.side_effect = _pull_side_effect

    manager = RepositoryManager()
    result = manager.clone_or_pull(_REPO_URL, dest)

    assert result is True
    mock_repo_cls.assert_called_once_with(dest)
    mock_repo.remotes.origin.pull.assert_called_once()
    # A pull must never fall back to cloning.
    mock_repo_cls.clone_from.assert_not_called()


@patch("core.git_manager.git.Repo")
def test_pull_returns_false_when_up_to_date(
    mock_repo_cls: MagicMock,
    tmp_path: Path,
) -> None:
    """A pull that leaves ``HEAD`` unchanged returns ``False``."""
    dest = tmp_path / "repo"
    dest.mkdir()
    (dest / "marker").write_text("x", encoding="utf-8")  # make non-empty

    mock_repo = MagicMock()
    mock_repo_cls.return_value = mock_repo
    mock_repo.head.commit.hexsha = "abc123"
    mock_repo.remotes.origin.pull.return_value = []

    manager = RepositoryManager()
    result = manager.clone_or_pull(_REPO_URL, dest)

    assert result is False
    mock_repo_cls.assert_called_once_with(dest)
    mock_repo.remotes.origin.pull.assert_called_once()
    mock_repo_cls.clone_from.assert_not_called()


@patch("core.git_manager.git.Repo")
def test_pull_preserves_head_commit_sha_for_comparison(
    mock_repo_cls: MagicMock,
    tmp_path: Path,
) -> None:
    """The before/after ``HEAD`` shas are read around the pull call.

    Ensures the implementation captures the commit *before* pulling and
    re-reads it *after*, rather than caching a stale value.
    """
    dest = tmp_path / "repo"
    dest.mkdir()
    (dest / "marker").write_text("x", encoding="utf-8")

    mock_repo = MagicMock()
    mock_repo_cls.return_value = mock_repo

    sha_reads: list[str] = []

    class _CommitStub:
        def __init__(self, sha: str) -> None:
            self.hexsha = sha

    class _HeadStub:
        def __init__(self) -> None:
            self._sha = "abc123"

        @property
        def commit(self) -> _CommitStub:
            sha = self._sha
            sha_reads.append(sha)
            return _CommitStub(sha)

    head = _HeadStub()
    mock_repo.head = head  # type: ignore[assignment]

    def _pull_side_effect(*_args: object, **_kwargs: object) -> list[object]:
        head._sha = "def456"
        return []

    mock_repo.remotes.origin.pull.side_effect = _pull_side_effect

    manager = RepositoryManager()
    result = manager.clone_or_pull(_REPO_URL, dest)

    assert result is True
    # Exactly two reads: one before pull, one after.
    assert sha_reads == ["abc123", "def456"]


@patch("core.git_manager.git.Repo")
def test_clone_with_branch_when_dest_does_not_exist(
    mock_repo_cls: MagicMock,
    tmp_path: Path,
) -> None:
    """A destination and branch URL triggers clone with branch and returns ``True``."""
    dest = tmp_path / "repo"
    mock_repo_cls.clone_from.return_value = MagicMock()

    manager = RepositoryManager()
    result = manager.clone_or_pull(f"{_REPO_URL}#dev", dest)

    assert result is True
    mock_repo_cls.clone_from.assert_called_once_with(_REPO_URL, dest, branch="dev")
    mock_repo_cls.assert_not_called()


@patch("core.git_manager.git.Repo")
def test_pull_with_branch_when_dest_is_not_empty(
    mock_repo_cls: MagicMock,
    tmp_path: Path,
) -> None:
    """A pull with a branch checks out the branch and pulls that branch."""
    dest = tmp_path / "repo"
    dest.mkdir()
    (dest / "marker").write_text("x", encoding="utf-8")  # make non-empty

    mock_repo = MagicMock()
    mock_repo_cls.return_value = mock_repo
    mock_repo.head.commit.hexsha = "abc123"

    def _pull_side_effect(*_args: object, **_kwargs: object) -> list[object]:
        mock_repo.head.commit.hexsha = "def456"
        return []

    mock_repo.remotes.origin.pull.side_effect = _pull_side_effect

    manager = RepositoryManager()
    result = manager.clone_or_pull(f"{_REPO_URL}#dev", dest)

    assert result is True
    mock_repo_cls.assert_called_once_with(dest)
    mock_repo.git.checkout.assert_called_once_with("dev")
    mock_repo.remotes.origin.pull.assert_called_once_with("dev")
    mock_repo_cls.clone_from.assert_not_called()


@patch("core.git_manager.git.Repo")
def test_pull_with_branch_returns_false_when_up_to_date(
    mock_repo_cls: MagicMock,
    tmp_path: Path,
) -> None:
    """A pull with a branch that leaves ``HEAD`` unchanged returns ``False``."""
    dest = tmp_path / "repo"
    dest.mkdir()
    (dest / "marker").write_text("x", encoding="utf-8")  # make non-empty

    mock_repo = MagicMock()
    mock_repo_cls.return_value = mock_repo
    mock_repo.head.commit.hexsha = "abc123"
    mock_repo.remotes.origin.pull.return_value = []

    manager = RepositoryManager()
    result = manager.clone_or_pull(f"{_REPO_URL}#dev", dest)

    assert result is False
    mock_repo_cls.assert_called_once_with(dest)
    mock_repo.git.checkout.assert_called_once_with("dev")
    mock_repo.remotes.origin.pull.assert_called_once_with("dev")
    mock_repo_cls.clone_from.assert_not_called()


if __name__ == "__main__":
    pytest.main([__file__, "-v"])
