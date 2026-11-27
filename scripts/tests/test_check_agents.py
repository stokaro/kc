import importlib.util
import tempfile
import unittest
from pathlib import Path

SPEC = importlib.util.spec_from_file_location(
    "check_agents", Path(__file__).resolve().parents[1] / "check-agents.py"
)
CHECK = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(CHECK)


class InstructionChecks(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        for name in ("AGENTS.md", "CONTRIBUTING.md", "internal/table/AGENTS.md",
                     "docs/agent-workflow.md", ".agents/licenses/superpowers-MIT.txt"):
            self.write(name, "# Instructions\n")
        self.write("CLAUDE.md", "@AGENTS.md\n")
        for name in CHECK.SKILLS:
            self.write(f".agents/skills/{name}/SKILL.md",
                       f"---\nname: {name}\ndescription: Use for changes; skip prose.\n---\n\n# Skill\n")
        (self.root / ".claude").mkdir()
        (self.root / ".claude/skills").symlink_to("../.agents/skills")

    def write(self, name, content):
        path = self.root / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(content, encoding="utf-8")

    def assert_rejected(self, fragment):
        self.assertTrue(any(fragment in error for error in CHECK.check(self.root)),
                        CHECK.check(self.root))

    def test_valid_shared_setup(self):
        self.assertEqual(CHECK.check(self.root), [])

    def test_broken_reference_detected_in_optional_material(self):
        self.write(".agents/skills/kc-go/references/errors.md", "[source](missing.md)\n")
        self.assert_rejected("missing local link")

    def test_links_and_anchors_resolve_from_their_document(self):
        self.write(".agents/skills/kc-go/references/errors.md",
                   "[core](../SKILL.md#errors)\n[web](https://go.dev/doc/)\n[section](#a)\n")
        self.assertEqual(CHECK.check(self.root), [])

    def test_example_links_are_not_loaded(self):
        fence = "\x60" * 3
        self.write("CONTRIBUTING.md", f"{fence}markdown\n[example](not-a-real-file.md)\n{fence}\n")
        self.assertEqual(CHECK.check(self.root), [])

    def test_reference_cannot_escape_repo(self):
        self.write("AGENTS.md", "[outside](../outside.md)\n")
        self.assert_rejected("escapes")

    def test_budget_counts_utf8_bytes_not_lines(self):
        self.write("AGENTS.md", "界" * 1800)
        self.assert_rejected("exceeds")

    def test_skill_budget_prevents_implicit_reference_dump(self):
        name = ".agents/skills/kc-go/SKILL.md"
        self.write(name, (self.root / name).read_text() + "x" * CHECK.SKILL_BUDGET)
        self.assert_rejected("exceeds core budget")

    def test_duplicate_or_mismatched_metadata_fails(self):
        self.write(".agents/skills/kc-go/SKILL.md",
                   "---\nname: wrong\nname: kc-tui\ndescription: Use when needed.\n---\n")
        self.assert_rejected("duplicate name")
        self.assert_rejected("name must match")

    def test_invalid_yaml_is_not_accepted_as_a_description(self):
        self.write(".agents/skills/kc-go/SKILL.md",
                   "---\nname: kc-go\ndescription: Use when: needed\n---\n")
        self.assert_rejected("unsupported YAML")

    def test_copied_bridge_does_not_silently_drift(self):
        bridge = self.root / ".claude/skills"
        bridge.unlink()
        bridge.mkdir()
        self.assert_rejected("must symlink")

    def test_claude_wrapper_cannot_duplicate_root(self):
        self.write("CLAUDE.md", "@AGENTS.md\n\nMore mandatory rules.\n")
        self.assert_rejected("only import")

    def test_missing_skill_or_license_is_rejected(self):
        (self.root / ".agents/skills/kc-tui/SKILL.md").unlink()
        (self.root / ".agents/licenses/superpowers-MIT.txt").unlink()
        self.assert_rejected("canonical skills")
        self.assert_rejected("license")


if __name__ == "__main__":
    unittest.main()
