"""Logging configuration for the CMDP Doc Bot.

Provides a small wrapper around the standard library ``logging`` module
to keep log formatting consistent across the application.
"""

from __future__ import annotations

import logging
import sys

_DEFAULT_FORMAT = "%(asctime)s - %(name)s - %(levelname)s - %(message)s"
_DEFAULT_LEVEL = logging.INFO

_CONFIGURED = False


def setup_logging(level: int = _DEFAULT_LEVEL) -> None:
    """Configure root logger output for the application.

    Installs a single stream handler writing to ``stderr`` with a
    consistent timestamped format. Safe to call multiple times: only the
    first call has an effect, so repeated initialization does not
    duplicate handlers.

    Args:
        level: The logging level for the root logger. Defaults to
            :data:`logging.INFO`.
    """
    global _CONFIGURED
    if _CONFIGURED:
        return

    handler = logging.StreamHandler(stream=sys.stderr)
    handler.setFormatter(logging.Formatter(_DEFAULT_FORMAT))

    root = logging.getLogger()
    root.setLevel(level)
    root.addHandler(handler)
    _CONFIGURED = True


def get_logger(name: str) -> logging.Logger:
    """Return a named logger using the application's configuration.

    Ensures logging has been initialized before handing back a logger,
    so modules can call :func:`get_logger` at import time without first
    invoking :func:`setup_logging`.

    Args:
        name: The logger name, typically ``__name__`` of the calling
            module.

    Returns:
        A configured :class:`logging.Logger` instance.
    """
    if not _CONFIGURED:
        setup_logging()
    return logging.getLogger(name)


def reset_logging() -> None:
    """Reset logging state to unconfigured.

    Intended for tests that need to reinitialize logging from a clean
    state. Removes all handlers from the root logger.
    """
    global _CONFIGURED
    root = logging.getLogger()
    for handler in list(root.handlers):
        root.removeHandler(handler)
    _CONFIGURED = False
