#!/usr/bin/env python3
"""Conservatively select local Go tests; this does not replace full CI."""

from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import shlex
import subprocess
import sys
from dataclasses import dataclass


class CommandError(RuntimeError):
    """A command could not provide the information needed for selection."""


@dataclass(frozen=True)
class Plan:
    packages: tuple[str, ...]
    reasons: tuple[str, ...]


def capture(args: list[str], root: Path) -> str:
    try:
        result = subprocess.run(
            args, cwd=root, capture_output=True, text=True,
            encoding="utf-8", errors="surrogateescape", check=False,
        )
    except OSError as error:
        raise CommandError(f"{args[0]}: {error}") from error
    if result.returncode:
        detail = " ".join(result.stderr.split())[:500]
        raise CommandError(f"{shlex.join(args)} exited {result.returncode}: {detail}")
    return result.stdout


def repository_root() -> Path:
    return Path(capture(["git", "rev-parse", "--show-toplevel"], Path.cwd()).strip())


def changed_files(root: Path, base: str) -> list[str]:
    ancestor = capture(["git", "merge-base", "--", "HEAD", base], root).strip()
    tracked = capture(
        ["git", "diff", "--name-only", "-z", "--no-renames", ancestor, "--"], root,
    )
    untracked = capture(["git", "ls-files", "--others", "--exclude-standard", "-z"], root)
    return sorted(set(filter(None, (tracked + untracked).split("\0"))))


def json_objects(text: str) -> list[dict]:
    decoder = json.JSONDecoder()
    objects = []
    offset = 0
    while offset < len(text):
        while offset < len(text) and text[offset].isspace():
            offset += 1
        if offset == len(text):
            break
        value, offset = decoder.raw_decode(text, offset)
        if not isinstance(value, dict):
            raise ValueError("go list returned a non-object package")
        objects.append(value)
    return objects


def load_packages(root: Path) -> list[dict]:
    return json_objects(capture(["go", "list", "-json", "./..."], root))


def documentation(path: str) -> bool:
    parts = Path(path).parts
    if "testdata" in parts:
        return False
    if Path(path).suffix.lower() in {".md", ".markdown", ".rst", ".adoc"}:
        return True
    names = {"LICENSE", "COPYING", "NOTICE", "AUTHORS", "README"}
    if Path(path).name.upper() in names:
        return True
    if Path(path).suffix.lower() == ".txt":
        return Path(path).stem.upper() in names or path.startswith(("docs/", ".agents/licenses/"))
    return bool(parts and parts[0] in {"docs", "screenshots"}
                and Path(path).suffix.lower() in {".png", ".svg", ".gif", ".jpg", ".jpeg"})


def possible_embed(root: Path, path: str) -> bool:
    """Keep documentation cheap without skipping Go-embedded documentation."""
    directory = (root / path).parent
    while directory == root or root in directory.parents:
        for source in directory.glob("*.go"):
            try:
                if "//go:embed" in source.read_text(encoding="utf-8", errors="replace"):
                    return True
            except OSError:
                return True  # An unreadable source cannot establish a docs-only change.
        if directory == root:
            break
        directory = directory.parent
    return False


def global_change(path: str) -> bool:
    if path in {
        "go.mod", "go.sum", "go.work", "go.work.sum", "Makefile", "makefile",
        "GNUmakefile", "Dockerfile", "config-default.yaml", ".golangci.yml",
        ".golangci.yaml", ".goreleaser.yml", ".goreleaser.yaml",
    }:
        return True
    return not documentation(path) and path.startswith((
        ".github/workflows/", ".github/actions/", "scripts/", "tools/", "hack/",
        "build/", ".devcontainer/",
    ))


def full(reason: str) -> Plan:
    return Plan(("./...",), (reason,))


def relative_dir(root: Path, package: dict) -> str:
    directory = Path(package["Dir"]).resolve().relative_to(root.resolve())
    return directory.as_posix() if directory.parts else ""


def under(path: str, directory: str) -> bool:
    return not directory or path == directory or path.startswith(directory + "/")


def embedded(path: str, directory: str, package: dict) -> bool:
    if not under(path, directory):
        return False
    name = path[len(directory) + 1:] if directory else path
    for field in ("EmbedFiles", "TestEmbedFiles", "XTestEmbedFiles"):
        if name in package.get(field, []):
            return True
    for field in ("EmbedPatterns", "TestEmbedPatterns", "XTestEmbedPatterns"):
        for pattern in package.get(field, []):
            pattern = pattern[4:] if pattern.startswith("all:") else pattern
            wildcard = next((i for i, char in enumerate(pattern) if char in "*?["), None)
            # A prefix deliberately over-selects. This covers new/removed files
            # absent from EmbedFiles and directory patterns' recursive contents.
            if wildcard is not None and name.startswith(pattern[:wildcard]):
                return True
            if wildcard is None and under(name, pattern.rstrip("/")):
                return True
    return False


def select_tests(root: Path, paths: list[str]) -> Plan:
    if not paths:
        return Plan((), ("no changes against the merge base",))
    for path in paths:
        if global_change(path):
            return full(f"shared build/configuration/tooling changed: {path}")
    docs = {path for path in paths if documentation(path) and not possible_embed(root, path)}
    if len(docs) == len(paths):
        return Plan((), ("documentation-only change; no Go toolchain needed",))

    try:
        metadata = load_packages(root)
        packages = {}
        directories = {}
        for package in metadata:
            try:
                directory = relative_dir(root, package)
            except ValueError:
                continue  # Only packages within this checkout belong to the graph.
            name = package["ImportPath"]
            packages[name] = package
            directories[name] = directory
    except (CommandError, ValueError, KeyError, TypeError) as error:
        return full(f"package metadata unavailable; conservative fallback: {error}")
    if not packages:
        return full("go list returned no in-repository packages")

    owners_by_dir = {directory: name for name, directory in directories.items()}
    selected = set()
    reasons = set()
    for path in paths:
        if path in docs:
            continue
        if Path(path).suffix == ".go":
            directory = Path(path).parent.as_posix()
            owner = owners_by_dir.get("" if directory == "." else directory)
            if owner is None:
                return full(f"Go package removed, missing or excluded from the build graph: {path}")
            if Path(path).name in packages[owner].get("IgnoredGoFiles", []):
                return full(f"Go file is outside the current build configuration: {path}")
            owners = {owner}
        else:
            owners = {
                name for name, package in packages.items()
                if embedded(path, directories[name], package)
            }
            if "testdata" in Path(path).parts:
                candidates = [directory for directory in owners_by_dir if under(path, directory)]
                if candidates:
                    owners.add(owners_by_dir[max(candidates, key=len)])
            if not owners:
                if documentation(path):
                    continue  # Metadata confirms it was not an embedded document.
                return full(f"non-documentation input has no known package owner: {path}")
        selected.update(owners)
        reasons.update(f"changed inputs in {name}" for name in owners)

    reverse = {name: set() for name in packages}
    for name, package in packages.items():
        for field in ("Imports", "TestImports", "XTestImports"):
            for imported in package.get(field, []):
                if imported in reverse:
                    reverse[imported].add(name)
    pending = list(selected)
    while pending:
        for importer in reverse[pending.pop()] - selected:
            selected.add(importer)
            pending.append(importer)
    if selected:
        reasons.add("including in-module reverse importers and test importers")
    else:
        reasons.add("only documentation outside embedded inputs changed")
    arguments = tuple(sorted("./" + directories[name] if directories[name] else "." for name in selected))
    return Plan(arguments, tuple(sorted(reasons)))


def test_command(plan: Plan, mode: str, timeout: str) -> list[str]:
    command = ["go", "test", f"-timeout={timeout}"]
    if mode == "race":
        command.append("-race")
    return command + list(plan.packages)


def run_tests(root: Path, plan: Plan, mode: str, timeout: str) -> int:
    environment = os.environ.copy()
    required = mode == "integration"
    environment.pop("KC_SKIP_ENVTEST" if required else "KC_REQUIRE_ENVTEST", None)
    environment["KC_REQUIRE_ENVTEST" if required else "KC_SKIP_ENVTEST"] = "1"
    try:
        return subprocess.run(
            test_command(plan, mode, timeout), cwd=root, env=environment, check=False,
        ).returncode
    except OSError as error:
        raise CommandError(f"go test: {error}") from error


def main(argv=None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--plan", action="store_true", help="show selection without running tests")
    parser.add_argument("--base", default="origin/main", help="Git base ref (default: origin/main)")
    parser.add_argument("--mode", choices=("unit", "integration", "race"), default="unit")
    parser.add_argument("--timeout", default="5m", help="Go test timeout per package (default: 5m)")
    args = parser.parse_args(argv)
    try:
        root = repository_root()
        paths = changed_files(root, args.base)
        plan = select_tests(root, paths)
        print(f"Changed files: {len(paths)}; mode: {args.mode}; base: {args.base}")
        for reason in plan.reasons[:5]:
            print(f"- {reason}")
        if len(plan.reasons) > 5:
            print(f"- {len(plan.reasons) - 5} additional changed package(s)")
        if not plan.packages:
            print("No Go tests selected. Run applicable documentation/agent checks separately.")
            return 0
        print("Command: " + shlex.join(test_command(plan, args.mode, args.timeout)))
        print("Envtest: required" if args.mode == "integration" else "Envtest: disabled")
        print("Selection covers the current Go build configuration; full CI remains required.", flush=True)
        if args.plan:
            return 0
        return run_tests(root, plan, args.mode, args.timeout)
    except CommandError as error:
        print(f"test-changed: {error}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    sys.exit(main())
