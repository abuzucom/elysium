#!/usr/bin/env python3
"""Resolve GitHub CLI outside the repository and return bounded account data."""
import json
import os
import re
import subprocess
import sys
import tempfile
import urllib.parse
from pathlib import Path


ACCOUNT_OUTPUT_LIMIT = 256
COMMAND_OUTPUT_LIMIT = 1024 * 1024
METADATA_OUTPUT_LIMIT = 65536
GH_TIMEOUT_SECONDS = 5
# Parent directories examined for a .git entry before repository context
# discovery gives up.
MAX_PARENT_SEARCH_DEPTH = 100
PROXY_VARIABLES = ("HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY",
                   "http_proxy", "https_proxy", "all_proxy")
MANAGED_PROXY_HOST = "127.0.0.1"
MANAGED_PROXY_PORT = 9
LOGIN = re.compile(r"\A[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?\Z")
TEXT_OPTIONS = frozenset(("--body", "--title"))
REPOSITORY_COMMANDS = frozenset(("pr", "issue", "run"))
REPOSITORY_NAME = re.compile(
    r"\A[A-Za-z0-9](?:[A-Za-z0-9._-]{0,98}[A-Za-z0-9])?\Z"
)
BRANCH_NAME = re.compile(r"\A[A-Za-z0-9][A-Za-z0-9._/-]{0,199}\Z")
# C0 controls and DEL, except tab, line feed, and carriage return.
TERMINAL_CONTROL_PATTERN = re.compile(r"[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]")
GLOBAL_VALUE_OPTIONS = frozenset(("-R", "--repo", "--hostname"))


def find_literal_escape_sequences(arguments: list[str]) -> list[str]:
    """Return prose options containing escape text instead of real newlines."""
    findings = []
    for index, argument in enumerate(arguments):
        if argument in TEXT_OPTIONS:
            if index + 1 < len(arguments) and "\\n" in arguments[index + 1]:
                findings.append(argument)
            continue
        for option in TEXT_OPTIONS:
            if argument.startswith(f"{option}=") and "\\n" in argument:
                findings.append(option)
    return findings


def _find_git_entry(start: Path) -> Path:
    """Return the nearest .git entry or raise a bounded error."""
    current = start.resolve()
    for _ in range(MAX_PARENT_SEARCH_DEPTH):
        candidate = current / ".git"
        if candidate.is_dir() or candidate.is_file():
            return candidate
        parent = current.parent
        if parent == current:
            break
        current = parent
    raise ValueError("repository context is missing; run from a Git checkout")


def _git_config_path(git_entry: Path) -> Path:
    """Return a local Git config path for a directory or worktree pointer."""
    if git_entry.is_dir():
        return git_entry / "config"
    git_dir = _git_dir(git_entry)
    common_file = git_dir / "commondir"
    if common_file.is_file():
        if common_file.stat().st_size > METADATA_OUTPUT_LIMIT:
            raise ValueError("repository context common pointer exceeds the safety limit")
        common_value = common_file.read_text(
            encoding="utf-8", errors="strict"
        ).strip()
        if not common_value or "\n" in common_value or "\r" in common_value:
            raise ValueError("repository context has an invalid common Git directory")
        common_dir = (git_dir / common_value).resolve()
        if not common_dir.is_dir():
            raise ValueError("repository context has a missing common Git directory")
        return common_dir / "config"
    return git_dir / "config"


def _git_dir(git_entry: Path) -> Path:
    """Return the resolved administrative Git directory."""
    if git_entry.is_dir():
        return git_entry
    if git_entry.stat().st_size > METADATA_OUTPUT_LIMIT:
        raise ValueError("repository context Git pointer exceeds the safety limit")
    content = git_entry.read_text(encoding="utf-8", errors="strict")
    marker, separator, value = content.strip().partition(":")
    if marker.strip().lower() != "gitdir" or not separator or not value.strip():
        raise ValueError("repository context has an invalid Git worktree pointer")
    git_dir = (git_entry.parent / value.strip()).resolve()
    if not git_dir.is_dir():
        raise ValueError("repository context has a missing Git worktree directory")
    return git_dir


def _origin_url(config_path: Path) -> str:
    """Read the origin URL from a bounded local Git config."""
    if config_path.stat().st_size > METADATA_OUTPUT_LIMIT:
        raise ValueError("repository context Git config exceeds the safety limit")
    section = ""
    origin = ""
    for line in config_path.read_text(encoding="utf-8", errors="strict").splitlines():
        stripped = line.strip()
        if stripped.startswith("[") and stripped.endswith("]"):
            section = stripped.lower()
            continue
        key, separator, value = stripped.partition("=")
        if separator and section == '[remote "origin"]' and key.strip().lower() == "url":
            origin = value.strip()
    if not origin:
        raise ValueError("repository context has no origin remote")
    return origin


def _repository_from_origin(origin: str) -> str:
    """Return a validated GitHub OWNER/REPOSITORY target."""
    if any(character in origin for character in "\r\n\x00"):
        raise ValueError("repository context contains unsafe origin metadata")
    value = origin.strip()
    if value.startswith("https://") or value.startswith("ssh://"):
        parsed = urllib.parse.urlsplit(value)
        if (parsed.hostname != "github.com" or parsed.password
                or parsed.username not in (None, "git")):
            raise ValueError("repository origin is not a safe GitHub remote")
        path = parsed.path.lstrip("/")
    elif value.startswith("git@github.com:"):
        path = value.removeprefix("git@github.com:")
    else:
        raise ValueError("repository origin is not a supported GitHub remote")
    path = path.removesuffix(".git")
    parts = path.split("/")
    if len(parts) != 2 or not all(REPOSITORY_NAME.fullmatch(part) for part in parts):
        raise ValueError("repository origin has an invalid owner or repository")
    return "/".join(parts)


def repository_target(start: Path) -> str:
    """Return the validated GitHub target for the checkout containing start."""
    git_entry = _find_git_entry(start)
    return _repository_from_origin(_origin_url(_git_config_path(git_entry)))


def repository_branch(start: Path) -> str:
    """Return the validated current branch from local Git metadata."""
    head_path = _git_dir(_find_git_entry(start)) / "HEAD"
    if head_path.stat().st_size > METADATA_OUTPUT_LIMIT:
        raise ValueError("repository HEAD exceeds the safety limit")
    content = head_path.read_text(encoding="utf-8", errors="strict").strip()
    marker = "ref: refs/heads/"
    if not content.startswith(marker):
        raise ValueError("repository has no named branch; check out a branch first")
    branch = content.removeprefix(marker)
    components = branch.split("/")
    if (not BRANCH_NAME.fullmatch(branch) or any(component in ("", ".", "..")
                                                for component in components)):
        raise ValueError("repository has an invalid current branch")
    return branch


def _has_repository_option(arguments: list[str]) -> bool:
    """Return whether arguments contain a structural repository option."""
    for index, argument in enumerate(arguments):
        if argument in ("-R", "--repo") and index + 1 < len(arguments):
            return True
        if argument.startswith("--repo=") or argument.startswith("-R") and len(argument) > 2:
            return True
    return False


def _command_position(arguments: list[str]) -> int:
    """Return the first GitHub command token after global options."""
    index = 0
    while index < len(arguments):
        argument = arguments[index]
        if argument == "--":
            return index + 1
        if argument in GLOBAL_VALUE_OPTIONS:
            index += 2
            continue
        if argument.startswith("--") and "=" in argument:
            index += 1
            continue
        if argument.startswith("-"):
            index += 1
            continue
        return index
    return len(arguments)


def with_repository_context(repository: Path, arguments: list[str]) -> list[str]:
    """Add validated repository context to a repository-bound command."""
    position = _command_position(arguments)
    if (position >= len(arguments) or arguments[position] not in REPOSITORY_COMMANDS
            or _has_repository_option(arguments)):
        return list(arguments)
    target = repository_target(repository)
    delimiter = arguments.index("--") if "--" in arguments else len(arguments)
    context = [*arguments[:delimiter], "--repo", target, *arguments[delimiter:]]
    command = arguments[position:position + 2]
    has_head = any(argument == "--head" or argument.startswith("--head=")
                   for argument in arguments)
    if command == ["pr", "create"] and not has_head:
        context[delimiter:delimiter] = (
            "--head", f"{target.split('/')[0]}:{repository_branch(repository)}"
        )
    return context


def _is_inside(path: Path, directory: Path) -> bool:
    """Return whether one path resides under a directory."""
    try:
        path.relative_to(directory)
        return True
    except ValueError:
        return False


def _candidate_names() -> tuple[str, ...]:
    """Return accepted GitHub CLI executable names."""
    if os.name == "nt":
        os.environ["NoDefaultCurrentDirectoryInExePath"] = "1"
        return ("gh.exe", "gh.com")
    return ("gh",)


def resolve_gh(repo_root) -> str:
    """Return an absolute GitHub CLI executable outside the repository."""
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
    raise FileNotFoundError("trusted GitHub CLI executable was not found on PATH")


def _safe_directory(repository: Path, executable: Path) -> Path:
    """Return an external execution directory."""
    for candidate in (Path(tempfile.gettempdir()), executable.parent):
        try:
            resolved = candidate.resolve(strict=True)
        except OSError:
            continue
        if resolved.is_dir() and not _is_inside(resolved, repository):
            return resolved
    raise OSError("no safe external directory is available for GitHub CLI")


def _safe_search_path(repository: Path) -> str:
    """Return PATH without relative or repository-controlled entries."""
    entries = []
    for raw_directory in os.environ.get("PATH", "").split(os.pathsep):
        directory = Path(raw_directory.strip('"')) if raw_directory else Path()
        if not raw_directory or not directory.is_absolute():
            continue
        try:
            resolved = directory.resolve(strict=False)
        except OSError:
            continue
        if not _is_inside(resolved, repository):
            entries.append(str(resolved))
    return os.pathsep.join(entries)


def _is_managed_proxy_placeholder(value: str) -> bool:
    """Return whether a proxy value is the managed Codex placeholder."""
    candidate = value.strip()
    try:
        parsed = urllib.parse.urlsplit(candidate if "://" in candidate
                                       else "//" + candidate)
        port = parsed.port
    except ValueError:
        return False
    return parsed.hostname == MANAGED_PROXY_HOST and port == MANAGED_PROXY_PORT


def _sanitize_proxy_environment(environment: dict) -> None:
    """Remove only managed proxy placeholders from an environment."""
    for variable in PROXY_VARIABLES:
        value = environment.get(variable)
        if value and _is_managed_proxy_placeholder(value):
            environment.pop(variable, None)


def _safe_environment(repository: Path) -> dict:
    """Return an environment without repository-controlled execution paths."""
    environment = dict(os.environ)
    environment.pop("GH_CONFIG_DIR", None)
    environment.pop("GH_REPO", None)
    _sanitize_proxy_environment(environment)
    environment.update({"GH_PAGER": "", "GH_PROMPT_DISABLED": "1"})
    environment["PATH"] = _safe_search_path(repository)
    if os.name == "nt":
        environment["NoDefaultCurrentDirectoryInExePath"] = "1"
    return environment


def run_gh(repo_root, arguments: list[str], *, runner=None, timeout=None):
    """Run trusted GitHub CLI from outside the repository."""
    repository = Path(repo_root).resolve()
    executable = Path(resolve_gh(repository))
    environment = _safe_environment(repository)
    execute = runner or subprocess.run
    return execute(
        [str(executable), *arguments],
        cwd=_safe_directory(repository, executable),
        env=environment,
        capture_output=True,
        text=True,
        encoding="utf-8",
        errors="replace",
        check=False,
        timeout=timeout,
    )


def parse_account(output: str) -> dict:
    """Parse a bounded numeric ID and GitHub login."""
    if len(output) > ACCOUNT_OUTPUT_LIMIT:
        raise ValueError("GitHub account output exceeds the bound")
    fields = output.strip().split("\t")
    if len(fields) != 2 or not fields[0].isdigit() or int(fields[0]) < 1:
        raise ValueError("GitHub account output has an invalid account ID")
    if not LOGIN.fullmatch(fields[1]):
        raise ValueError("GitHub account output has an invalid login")
    return {"id": int(fields[0]), "login": fields[1]}


def authenticated_account(repo_root) -> dict:
    """Return the authenticated GitHub account through a fixed API request."""
    result = run_gh(
        repo_root,
        ["api", "user", "--jq", "[.id,.login]|@tsv"],
        timeout=GH_TIMEOUT_SECONDS,
    )
    if result.returncode != 0:
        raise OSError("GitHub CLI has no authenticated account")
    return parse_account(result.stdout)


def _is_token_output(arguments: list[str]) -> bool:
    """Return whether the command requests an authentication token."""
    normalized = [argument.lower() for argument in arguments]
    if any(normalized[index:index + 2] == ["auth", "token"]
           for index in range(len(normalized) - 1)):
        return True
    for index in range(len(normalized) - 1):
        if normalized[index:index + 2] != ["auth", "status"]:
            continue
        for token in normalized[index + 2:]:
            long_flag = token in ("--show-token", "--show-token=true")
            short_flag = token == "-t" or (
                token.startswith("-") and not token.startswith("--")
                and "t" in token.split("=", 1)[0][1:])
            if long_flag or short_flag:
                return True
    return False


def _classify_failure(output: str) -> str:
    """Return a safe category for a GitHub CLI failure message."""
    lowered = output.lower()
    if "proxy" in lowered or "127.0.0.1:9" in lowered:
        return "proxy failure"
    if ("authentication" in lowered or "authenticated account" in lowered
            or "not logged" in lowered):
        return "authentication failure"
    return "GitHub CLI failure"


def _safe_error(error: BaseException) -> str:
    """Return an error without credential-like key-value values."""
    return _redact_sensitive_text(str(error))


def _redact_sensitive_text(message: str) -> str:
    """Return text without credential-like key-value values."""
    return re.sub(
        r"(?i)(token|password|secret|authorization)(\s*[:=]\s*)\S+",
        r"\1\2<redacted>",
        message,
    )


def _strip_terminal_controls(output: str) -> str:
    """Replace terminal control characters, keeping tabs and line breaks."""
    return TERMINAL_CONTROL_PATTERN.sub("?", output)


def _safe_output(output: str) -> str:
    """Return bounded command output without terminal control characters."""
    return _redact_sensitive_text(_strip_terminal_controls(output))


def _run_requested_command(repo_root, arguments: list[str]) -> int:
    """Run one authenticated GitHub CLI command with bounded output."""
    if not arguments:
        print("error: run requires GitHub CLI arguments", file=sys.stderr)
        return 2
    escape_options = find_literal_escape_sequences(arguments)
    if escape_options:
        options = ", ".join(escape_options)
        print(
            f"error: {options} contains literal escape text; use real newlines "
            "or --body-file",
            file=sys.stderr,
        )
        return 2
    if _is_token_output(arguments):
        print("error: GitHub authentication token output is denied", file=sys.stderr)
        return 2
    hooks_directory = Path(__file__).resolve().parent.parent / "hooks"
    sys.path.insert(0, str(hooks_directory))
    try:
        import _gate_core as gate_core
    except ImportError as error:
        print(
            f"error: GitHub safety policy is unavailable ({_safe_error(error)})",
            file=sys.stderr,
        )
        return 2
    decision, reason = gate_core.forge_verdict("gh", arguments)
    if decision == "deny":
        print(f"error: GitHub safety policy: {reason}", file=sys.stderr)
        return 2
    try:
        effective_arguments = with_repository_context(Path(repo_root), arguments)
        decision, reason = gate_core.forge_verdict("gh", effective_arguments)
        if decision == "deny":
            print(f"error: GitHub safety policy: {reason}", file=sys.stderr)
            return 2
        authenticated_account(repo_root)
        result = run_gh(repo_root, effective_arguments)
    except (OSError, subprocess.TimeoutExpired, ValueError) as error:
        category = _classify_failure(str(error))
        print(f"error: {category}; inspect connectivity and repository context", file=sys.stderr)
        return 1
    # gh prints titles, bodies, and comments that other accounts wrote. An
    # escape sequence there could rewrite the terminal or its title.
    sys.stdout.write(_strip_terminal_controls(result.stdout[:COMMAND_OUTPUT_LIMIT]))
    safe_stderr = _safe_output(result.stderr[:COMMAND_OUTPUT_LIMIT])
    sys.stderr.write(safe_stderr)
    if result.returncode != 0:
        print(f"error: {_classify_failure(safe_stderr)}", file=sys.stderr)
    return result.returncode


def main() -> int:
    """Print bounded authenticated account metadata as JSON."""
    if len(sys.argv) > 1:
        if sys.argv[1] != "run":
            print("error: expected 'run' or no arguments", file=sys.stderr)
            return 2
        return _run_requested_command(os.getcwd(), sys.argv[2:])
    try:
        account = authenticated_account(os.getcwd())
    except (OSError, subprocess.TimeoutExpired, ValueError) as error:
        category = _classify_failure(str(error))
        print(f"error: {category}; inspect connectivity and account state", file=sys.stderr)
        return 1
    print(json.dumps(account, sort_keys=True))
    return 0


if __name__ == "__main__":
    sys.exit(main())
