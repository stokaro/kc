#!/usr/bin/env python3
"""Validate the repository's small, shared agent instruction set (stdlib only)."""

import re
import sys
from pathlib import Path
from urllib.parse import unquote, urlsplit

SKILLS = ("kc-go", "kc-tui", "kc-kubernetes", "kc-verification")
ROOT_BUDGET = 5 * 1024
SKILL_BUDGET = 5 * 1024
LINK = re.compile(r"(?<!!)\[[^\]\n]+\]\(([^)\s]+)(?:\s+[^)]*)?\)")


def metadata_errors(path, text):
    """Our skills deliberately use two plain, single-line YAML scalar fields."""
    errors = []
    parts = text.split("---\n", 2)
    if len(parts) != 3 or parts[0]:
        return [f"{path}: missing YAML frontmatter"]
    fields = {}
    for line in parts[1].splitlines():
        match = re.fullmatch(r"([a-z-]+): (.+)", line)
        if not match:
            errors.append(f"{path}: use plain single-line name/description fields")
            continue
        key, value = match.groups()
        if key in fields:
            errors.append(f"{path}: duplicate {key}")
        fields[key] = value
        if ": " in value or " #" in value or value[0] in "\"'[{>&*!|%@\x60":
            errors.append(f"{path}: unsupported YAML scalar in {key}")
    if set(fields) != {"name", "description"}:
        errors.append(f"{path}: expected name and description only")
    name = fields.get("name", "")
    if not re.fullmatch(r"[a-z0-9]+(?:-[a-z0-9]+)*", name) or len(name) > 64:
        errors.append(f"{path}: invalid skill name")
    if name != path.parent.name:
        errors.append(f"{path}: name must match its directory")
    description = fields.get("description", "").strip()
    if not description or len(description) > 1024:
        errors.append(f"{path}: description must contain 1-1024 characters")
    return errors


def local_link_errors(root, path, text):
    errors = []
    # Examples inside fenced blocks are not instructions to load a real file.
    prose = re.sub(r"(?ms)^(\x60{3}|~~~).*?^\1[^\n]*$", "", text)
    for raw in LINK.findall(prose):
        link = urlsplit(raw.strip("<>"))
        if link.scheme or link.netloc or not link.path:
            continue
        target = (path.parent / unquote(link.path)).resolve()
        if not target.is_relative_to(root):
            errors.append(f"{path}: local link escapes the repository: {raw}")
        elif not target.exists():
            errors.append(f"{path}: missing local link: {raw}")
    return errors


def check(root):
    root = root.resolve()
    errors = []
    documents = [
        root / name
        for name in (
            "AGENTS.md", "CLAUDE.md", "CONTRIBUTING.md",
            "internal/table/AGENTS.md", "docs/agent-workflow.md",
        )
    ]
    skill_root = root / ".agents/skills"
    actual = {p.parent.name for p in skill_root.glob("*/SKILL.md")}
    if actual != set(SKILLS):
        errors.append(f"expected exactly these canonical skills: {', '.join(SKILLS)}")
    documents.extend(sorted(skill_root.rglob("*.md")))
    for path in documents:
        if not path.is_file():
            errors.append(f"{path}: missing instruction file")
            continue
        text = path.read_text(encoding="utf-8")
        size = len(text.encode("utf-8"))
        if path == root / "AGENTS.md" and size > ROOT_BUDGET:
            errors.append(f"AGENTS.md: {size} bytes exceeds {ROOT_BUDGET}")
        if path.name == "SKILL.md":
            errors.extend(metadata_errors(path, text))
            if size > SKILL_BUDGET or len(text.splitlines()) > 500:
                errors.append(f"{path}: exceeds core budget ({SKILL_BUDGET} bytes / 500 lines)")
        errors.extend(local_link_errors(root, path, text))
    wrapper = root / "CLAUDE.md"
    if wrapper.is_file() and wrapper.read_text().strip() != "@AGENTS.md":
        errors.append("CLAUDE.md must only import AGENTS.md; keep one instruction source")
    bridge = root / ".claude/skills"
    if not bridge.is_symlink() or bridge.resolve() != skill_root.resolve():
        errors.append(".claude/skills must symlink to ../.agents/skills (enable Git symlinks)")
    if not (root / ".agents/licenses/superpowers-MIT.txt").is_file():
        errors.append("missing Superpowers attribution license")
    return errors


def main():
    root = Path(__file__).resolve().parents[1]
    errors = check(root)
    if errors:
        print("\n".join(errors), file=sys.stderr)
        return 1
    size = (root / "AGENTS.md").stat().st_size
    print(f"Agent files OK: shared discovery, 4 skills, local links; root {size}/{ROOT_BUDGET} bytes")
    return 0


if __name__ == "__main__":
    sys.exit(main())
