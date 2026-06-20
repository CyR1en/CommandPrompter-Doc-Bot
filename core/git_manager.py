"""Git repository cloning and pulling for the CMDP Doc Bot.

Provides :class:`RepositoryManager`, a thin wrapper around GitPython used
to keep local working copies of upstream documentation repositories in
sync. The bot's indexing layer calls :meth:`RepositoryManager.clone_or_pull`
on each polling cycle to discover whether new commits need to be
re-indexed.
"""

from __future__ import annotations

from pathlib import Path

import git

from core.logger import get_logger

_logger = get_logger(__name__)


class RepositoryManager:
    """Manage local clones of upstream Git repositories.

    The manager is stateless: each call to :meth:`clone_or_pull` operates
    on an explicit destination directory, so a single instance can safely
    synchronize many repositories from the caller's side.

    A destination is treated as "fresh" when it does not exist or contains
    no filesystem entries, in which case the repository is cloned from
    scratch. Any non-empty destination is assumed to be an existing clone
    and is updated with ``git pull`` against its ``origin`` remote.
    """

    def clone_or_pull(self, repo_url: str, dest_dir: Path) -> bool:
        """Clone ``repo_url`` into ``dest_dir`` or pull the latest changes.

        If ``dest_dir`` does not exist or is empty, the repository is
        cloned fresh from ``repo_url``. Otherwise the existing clone is
        opened and ``origin`` is pulled, and the ``HEAD`` commit is
        compared before and after the pull to determine whether anything
        changed.

        If a branch is specified in the URL (using ``#branch_name`` suffix),
        that branch is cloned or pulled.

        Args:
            repo_url: The Git URL to clone from (e.g.
                ``https://github.com/org/repo.git`` or
                ``https://github.com/org/repo.git#dev``).
            dest_dir: The local directory that should hold the working
                copy. Parent directories are created if necessary for a
                fresh clone.

        Returns:
            ``True`` if new commits were fetched — either because a fresh
            clone was performed or because a pull moved ``HEAD`` — and
            ``False`` if the repository was already up to date.

        Raises:
            git.exc.GitCommandError: If the underlying ``git`` command
                fails (e.g. network error or authentication failure).
            git.exc.InvalidGitRepositoryError: If ``dest_dir`` is
                non-empty but is not a valid Git repository.
        """
        url = repo_url
        branch = None
        if "#" in repo_url:
            url, branch = repo_url.split("#", 1)

        if not self._is_fresh(dest_dir):
            return self._pull(dest_dir, branch)

        _logger.info("Cloning %s into %s", url, dest_dir)
        dest_dir.parent.mkdir(parents=True, exist_ok=True)
        if branch:
            git.Repo.clone_from(url, dest_dir, branch=branch)
        else:
            git.Repo.clone_from(url, dest_dir)
        return True

    @staticmethod
    def _is_fresh(dest_dir: Path) -> bool:
        """Return whether ``dest_dir`` should be (re)cloned.

        A destination is considered fresh when it does not exist or when
        it exists but contains no filesystem entries.

        Args:
            dest_dir: The candidate destination directory.

        Returns:
            ``True`` if the directory is absent or empty, ``False``
            otherwise.
        """
        if not dest_dir.exists():
            return True
        return not any(dest_dir.iterdir())

    def _pull(self, dest_dir: Path, branch: str | None = None) -> bool:
        """Pull the latest changes for an existing clone.

        Records the current ``HEAD`` commit, fetches and merges from the
        ``origin`` remote, then compares the new ``HEAD`` commit to
        decide whether the working copy advanced.

        Args:
            dest_dir: Directory of an existing Git clone.
            branch: Optional branch name to pull.

        Returns:
            ``True`` if ``HEAD`` changed after the pull, ``False`` if the
            repository was already up to date.

        Raises:
            git.exc.GitCommandError: If the pull fails.
            git.exc.InvalidGitRepositoryError: If ``dest_dir`` is not a
                valid Git repository.
        """
        repo = git.Repo(dest_dir)
        before = repo.head.commit.hexsha

        _logger.info("Pulling latest changes in %s", dest_dir)
        if branch:
            repo.git.checkout(branch)
            repo.remotes.origin.pull(branch)
        else:
            repo.remotes.origin.pull()

        after = repo.head.commit.hexsha
        changed = before != after
        if changed:
            _logger.info("HEAD moved %s -> %s in %s", before, after, dest_dir)
        else:
            _logger.info("Already up to date: %s", dest_dir)
        return changed
