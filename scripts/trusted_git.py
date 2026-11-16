#!/usr/bin/env python3
"""Resolve and run Git without repository-controlled executable lookup."""
import os
import subprocess
import tempfile
from pathlib import Path

SAFE_CONFIG = (
    "-c",
    "core.pager=",
    "-c",
    "pager.log=false",
    "-c",
    "log.showSignature=false",
    "-c",
    "core.fsmonitor=false",
    "-c",
    "diff.external=",
    "-c",
    "protocol.ext.allow=never",
)


def _is_inside(path: Path, directory: Path) -> bool:
    """Return whether `path` is within `directory`."""
    try:
        path.relative_to(directory)
        return True
    except ValueError:
        return False


def _candidate_names() -> tuple[str, ...]:
    """Return executable names accepted for this platform."""
    if os.name == "nt":
        os.environ["NoDefaultCurrentDirectoryInExePath"] = "1"
        return ("git.exe", "git.com")
    return ("git",)


def resolve_git(repo_root) -> str:
    """Return an absolute Git executable outside `repo_root`."""
    repository = Path(repo_root).resolve()
    names = _candidate_names()
    for raw_directory in os.environ.get("PATH", "").split(os.pathsep):
        if not raw_directory:
            continue
        directory = Path(raw_directory.strip('"'))
        if not directory.is_absolute():
            continue
        for name in names:
            candidate = directory / name
            if _is_inside(Path(os.path.abspath(candidate)), repository):
                continue
            try:
                if candidate.is_symlink() or not candidate.is_file():
                    continue
                resolved = candidate.resolve(strict=True)
            except OSError:
                continue
            if _is_inside(resolved, repository):
                continue
            if os.name != "nt" and not os.access(resolved, os.X_OK):
                continue
            return str(resolved)
    raise FileNotFoundError("trusted Git executable was not found on PATH")


def _safe_directory(repository: Path, executable: Path) -> Path:
    """Return an existing execution directory outside the repository."""
    for candidate in (Path(tempfile.gettempdir()), executable.parent):
        try:
            resolved = candidate.resolve(strict=True)
        except OSError:
            continue
        if resolved.is_dir() and not _is_inside(resolved, repository):
            return resolved
    raise OSError("no safe external directory is available for Git execution")


def _safe_search_path(repository: Path) -> str:
    """Return PATH without empty, relative, or repository-controlled entries."""
    safe_entries = []
    for raw_directory in os.environ.get("PATH", "").split(os.pathsep):
        if not raw_directory:
            continue
        directory = Path(raw_directory.strip('"'))
        if not directory.is_absolute():
            continue
        try:
            resolved = directory.resolve(strict=False)
        except OSError:
            continue
        if not _is_inside(resolved, repository):
            safe_entries.append(str(resolved))
    return os.pathsep.join(safe_entries)


def run_git(
    repo_root, arguments: list[str], *, input_text=None, check=False,
    runner=None, timeout=None,
):
    """Run trusted Git against `repo_root` from an external directory."""
    repository = Path(repo_root).resolve()
    executable = Path(resolve_git(repository))
    environment = dict(os.environ)
    for name in (
        "GIT_COMMON_DIR", "GIT_CONFIG", "GIT_CONFIG_COUNT",
        "GIT_CONFIG_PARAMETERS", "GIT_DIR", "GIT_INDEX_FILE",
        "GIT_OBJECT_DIRECTORY", "GIT_WORK_TREE",
    ):
        environment.pop(name, None)
    for name in list(environment):
        if name.startswith(("GIT_CONFIG_KEY_", "GIT_CONFIG_VALUE_")):
            environment.pop(name)
    environment["GIT_PAGER"] = ""
    environment["PAGER"] = ""
    environment["GIT_ATTR_NOSYSTEM"] = "1"
    environment["GIT_NO_LAZY_FETCH"] = "1"
    environment["GIT_NO_REPLACE_OBJECTS"] = "1"
    environment["GIT_OPTIONAL_LOCKS"] = "0"
    environment["GIT_PROTOCOL_FROM_USER"] = "0"
    environment["GIT_TERMINAL_PROMPT"] = "0"
    environment["PATH"] = _safe_search_path(repository)
    environment.pop("GIT_EXTERNAL_DIFF", None)
    if os.name == "nt":
        environment["NoDefaultCurrentDirectoryInExePath"] = "1"
    command = [
        str(executable),
        "-C",
        str(repository),
        "--no-pager",
        "--no-replace-objects",
        *SAFE_CONFIG,
        *arguments,
    ]
    execute = runner or subprocess.run
    return execute(
        command,
        cwd=_safe_directory(repository, executable),
        env=environment,
        input=input_text,
        capture_output=True,
        text=True,
        encoding="utf-8",
        errors="replace",
        check=check,
        timeout=timeout,
    )
