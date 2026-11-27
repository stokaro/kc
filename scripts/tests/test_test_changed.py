"""Selection tests use real Git changes and supplied Go metadata, never Go."""

from contextlib import redirect_stderr, redirect_stdout
import importlib.util
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest import mock


SCRIPT = Path(__file__).resolve().parents[1] / "test-changed.py"
SPEC = importlib.util.spec_from_file_location("kc_test_changed", SCRIPT)
selector = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = selector
SPEC.loader.exec_module(selector)


class RepositoryTest(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix="kc-test-changed-")
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.git("init", "-q")
        self.git("config", "user.name", "Selection Test")
        self.git("config", "user.email", "selection@example.invalid")
        self.git("config", "commit.gpgsign", "false")
        self.git("config", "core.hooksPath", str(self.root / "absent-hooks"))
        self.write("go.mod", "module example.test/kc\n\ngo 1.23\n")
        self.write(".gitignore", "ignored/\n")
        self.write("docs/guide.md", "A guide.\n")
        for name in ("a", "b", "c"):
            self.write(f"{name}/{name}.go", f"package {name}\n")
        self.commit("Initial fixture")
        self.git("update-ref", "refs/remotes/origin/main", "HEAD")

    def git(self, *args):
        result = subprocess.run(
            ["git", *args], cwd=self.root, capture_output=True, text=True, check=True,
        )
        return result.stdout

    def write(self, name, text):
        path = self.root / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text, encoding="utf-8")

    def commit(self, message):
        self.git("add", ".")
        self.git("commit", "-qm", message)

    def package(self, directory, **fields):
        name = "example.test/kc" + ("/" + directory if directory else "")
        return {"ImportPath": name, "Dir": str(self.root / directory), **fields}

    def plan(self, paths, packages):
        with mock.patch.object(selector, "load_packages", return_value=packages) as load:
            plan = selector.select_tests(self.root, paths)
        self.assertLessEqual(load.call_count, 1)
        return plan


class GitChangesTest(RepositoryTest):
    def test_committed_staged_unstaged_and_untracked_changes_are_combined(self):
        self.write("a/a.go", "package a\n// committed\n")
        self.commit("Committed change")
        self.write("b/b.go", "package b\n// staged\n")
        self.git("add", "b/b.go")
        self.write("c/c.go", "package c\n// unstaged\n")
        self.write("new/added.go", "package new\n")
        self.write("ignored/ignored.go", "package ignored\n")
        self.assertEqual(
            selector.changed_files(self.root, "origin/main"),
            ["a/a.go", "b/b.go", "c/c.go", "new/added.go"],
        )

    def test_rename_keeps_both_old_and_new_paths(self):
        self.git("mv", "a/a.go", "a/renamed.go")
        self.assertEqual(
            selector.changed_files(self.root, "origin/main"), ["a/a.go", "a/renamed.go"],
        )
        plan = self.plan(selector.changed_files(self.root, "origin/main"), [self.package("a")])
        self.assertEqual(plan.packages, ("./a",))

    def test_deleted_last_file_cannot_disappear_from_selection(self):
        (self.root / "a/a.go").unlink()
        plan = self.plan(
            selector.changed_files(self.root, "origin/main"), [self.package("b")],
        )
        self.assertEqual(plan.packages, ("./...",))
        self.assertIn("removed, missing", plan.reasons[0])

    def test_filenames_are_nul_delimited(self):
        self.write("docs/a name\nwith-newline.md", "Docs.\n")
        self.assertEqual(
            selector.changed_files(self.root, "origin/main"), ["docs/a name\nwith-newline.md"],
        )

    def test_changes_use_merge_base_not_the_tip_of_the_base_branch(self):
        self.git("branch", "base-branch")
        self.write("a/a.go", "package a\n// this branch\n")
        self.commit("Working branch")
        current = self.git("rev-parse", "HEAD").strip()
        self.git("checkout", "-q", "base-branch")
        self.write("b/b.go", "package b\n// unrelated base change\n")
        self.commit("Base advanced")
        self.git("checkout", "-q", "--detach", current)
        self.assertEqual(selector.changed_files(self.root, "base-branch"), ["a/a.go"])


class SelectionTest(RepositoryTest):
    def test_documentation_and_empty_changes_do_not_load_go(self):
        with mock.patch.object(selector, "load_packages", side_effect=AssertionError("Go not needed")):
            for paths in ([], ["README.md", "docs/guide.md", "docs/figure.png", "LICENSE"]):
                with self.subTest(paths=paths):
                    self.assertEqual(selector.select_tests(self.root, paths).packages, ())

    def test_reverse_normal_and_test_imports_are_transitive(self):
        packages = [
            self.package("a"),
            self.package("b", Imports=["example.test/kc/a", "fmt"]),
            self.package("c", TestImports=["example.test/kc/b"]),
            self.package("d", XTestImports=["example.test/kc/c"]),
            self.package("unrelated", Imports=["fmt"]),
        ]
        self.assertEqual(self.plan(["a/a.go"], packages).packages, ("./a", "./b", "./c", "./d"))

    def test_multiple_changed_packages_are_deduplicated(self):
        packages = [self.package("a"), self.package("b", Imports=["example.test/kc/a"])]
        self.assertEqual(self.plan(["b/b.go", "a/a.go"], packages).packages, ("./a", "./b"))

    def test_untracked_package_is_selected_from_current_metadata(self):
        self.write("fresh/fresh.go", "package fresh\n")
        plan = self.plan(selector.changed_files(self.root, "origin/main"), [self.package("fresh")])
        self.assertEqual(plan.packages, ("./fresh",))

    def test_deleted_file_with_surviving_package_stays_scoped(self):
        plan = self.plan(["a/deleted.go"], [self.package("a")])
        self.assertEqual(plan.packages, ("./a",))

    def test_documentation_like_basename_does_not_hide_go_source(self):
        self.assertEqual(self.plan(["a/README.go"], [self.package("a")]).packages, ("./a",))

    def test_build_config_and_tooling_widen_without_loading_go(self):
        with mock.patch.object(selector, "load_packages", side_effect=AssertionError("Go not needed")):
            for path in ("go.mod", "go.sum", "Makefile", "config-default.yaml", ".golangci.yml",
                         ".github/workflows/check.yml", "scripts/check.py", "tools/generate.go"):
                with self.subTest(path=path):
                    self.assertEqual(selector.select_tests(self.root, [path]).packages, ("./...",))

    def test_unknown_input_widens_instead_of_guessing_an_owner(self):
        for path in ("a/custom-config.yaml", "a/data.txt"):
            with self.subTest(path=path):
                plan = self.plan([path], [self.package("a")])
                self.assertEqual(plan.packages, ("./...",))

    def test_testdata_markdown_belongs_to_nearest_package(self):
        packages = [self.package("a"), self.package("a/nested"), self.package("b")]
        plan = self.plan(["a/nested/testdata/case.md"], packages)
        self.assertEqual(plan.packages, ("./a/nested",))

    def test_testdata_without_an_owner_widens(self):
        self.assertEqual(
            self.plan(["testdata/case.md"], [self.package("a")]).packages, ("./...",),
        )

    def test_embedded_markdown_is_not_treated_as_documentation(self):
        self.write("a/a.go", "package a\nimport _ \"embed\"\n//go:embed data/*.md\nvar data string\n")
        packages = [self.package("a", EmbedFiles=["data/help.md"]),
                    self.package("b", Imports=["example.test/kc/a"])]
        self.assertEqual(self.plan(["a/data/help.md"], packages).packages, ("./a", "./b"))

    def test_new_and_removed_embed_inputs_match_declared_prefix(self):
        self.write("a/a.go", "package a\n//go:embed data/*.md\n")
        packages = [self.package("a", EmbedPatterns=["data/*.md"], EmbedFiles=["data/existing.md"])]
        for path in ("a/data/new.md", "a/data/removed.md"):
            with self.subTest(path=path):
                self.assertEqual(self.plan([path], packages).packages, ("./a",))

    def test_directory_and_all_embed_patterns_include_descendants(self):
        packages = [self.package("a", EmbedPatterns=["all:assets"])]
        self.assertEqual(self.plan(["a/assets/nested/.state.json"], packages).packages, ("./a",))

    def test_test_and_external_test_embeds_are_inputs(self):
        for field in ("TestEmbedFiles", "XTestEmbedFiles"):
            with self.subTest(field=field):
                package = self.package("a", **{field: ["fixtures/data.json"]})
                self.assertEqual(self.plan(["a/fixtures/data.json"], [package]).packages, ("./a",))

    def test_test_embed_patterns_cover_removed_files(self):
        for field in ("TestEmbedPatterns", "XTestEmbedPatterns"):
            with self.subTest(field=field):
                package = self.package("a", **{field: ["fixtures/*.json"]})
                self.assertEqual(self.plan(["a/fixtures/deleted.json"], [package]).packages, ("./a",))

    def test_root_package_can_embed_docs(self):
        self.write("main.go", "package main\n//go:embed docs\n")
        package = self.package("", EmbedPatterns=["docs"], EmbedFiles=["docs/guide.md"])
        self.assertEqual(self.plan(["docs/guide.md"], [package]).packages, (".",))

    def test_unrelated_readme_in_embed_package_is_still_documentation(self):
        self.write("a/a.go", "package a\n//go:embed data/*.json\n")
        package = self.package("a", EmbedPatterns=["data/*.json"])
        self.assertEqual(self.plan(["a/README.md"], [package]).packages, ())

    def test_ignored_go_file_widens_current_platform_graph(self):
        package = self.package("a", IgnoredGoFiles=["platform_windows.go"])
        self.assertEqual(self.plan(["a/platform_windows.go"], [package]).packages, ("./...",))

    def test_metadata_failure_or_empty_graph_widens(self):
        with mock.patch.object(selector, "load_packages", side_effect=selector.CommandError("toolchain unavailable")):
            plan = selector.select_tests(self.root, ["a/a.go"])
            self.assertEqual(plan.packages, ("./...",))
            self.assertIn("toolchain unavailable", plan.reasons[0])
        self.assertEqual(self.plan(["a/a.go"], []).packages, ("./...",))

    def test_external_directory_is_not_an_in_module_owner(self):
        package = {"ImportPath": "dependency.test/a", "Dir": str(self.root.parent / "external")}
        self.assertEqual(self.plan(["a/a.go"], [package]).packages, ("./...",))


class CommandTest(RepositoryTest):
    def test_plan_does_not_execute_go_tests(self):
        self.write("a/a.go", "package a\n// changed\n")
        output = io.StringIO()
        with mock.patch.object(selector, "repository_root", return_value=self.root), \
                mock.patch.object(selector, "load_packages", return_value=[self.package("a")]), \
                mock.patch.object(selector, "run_tests") as run, redirect_stdout(output):
            self.assertEqual(selector.main(["--plan", "--mode", "race", "--timeout", "30s"]), 0)
        run.assert_not_called()
        self.assertIn("go test -timeout=30s -race ./a", output.getvalue())

    def test_docs_only_cli_does_not_need_go(self):
        self.write("docs/guide.md", "Changed guide.\n")
        with mock.patch.object(selector, "repository_root", return_value=self.root), \
                mock.patch.object(selector, "load_packages", side_effect=AssertionError("Go not needed")), \
                mock.patch.object(selector, "run_tests") as run, redirect_stdout(io.StringIO()):
            self.assertEqual(selector.main([]), 0)
        run.assert_not_called()

    def test_missing_base_fails_without_running_tests(self):
        error = io.StringIO()
        with mock.patch.object(selector, "repository_root", return_value=self.root), \
                mock.patch.object(selector, "run_tests") as run, redirect_stderr(error):
            self.assertEqual(selector.main(["--base", "missing-base"]), 2)
        run.assert_not_called()
        self.assertIn("merge-base", error.getvalue())

    def test_modes_remove_inherited_conflicts_and_preserve_go_environment(self):
        initial = {"KC_REQUIRE_ENVTEST": "1", "KC_SKIP_ENVTEST": "1", "GOCACHE": "/existing-cache"}
        for mode in ("unit", "integration", "race"):
            with self.subTest(mode=mode), mock.patch.dict(os.environ, initial, clear=True), \
                    mock.patch.object(selector.subprocess, "run", return_value=mock.Mock(returncode=7)) as run:
                self.assertEqual(selector.run_tests(self.root, selector.Plan(("./a",), ()), mode, "5m"), 7)
                args, kwargs = run.call_args
                environment = kwargs["env"]
                active = "KC_REQUIRE_ENVTEST" if mode == "integration" else "KC_SKIP_ENVTEST"
                inactive = "KC_SKIP_ENVTEST" if mode == "integration" else "KC_REQUIRE_ENVTEST"
                self.assertEqual(environment[active], "1")
                self.assertNotIn(inactive, environment)
                self.assertEqual(environment["GOCACHE"], "/existing-cache")
                self.assertEqual("-race" in args[0], mode == "race")
                self.assertNotIn("shell", kwargs)
                self.assertEqual(kwargs["cwd"], self.root)
                self.assertEqual(os.environ, initial)

    def test_package_names_are_arguments_not_shell_text(self):
        plan = selector.Plan(("./odd name;echo injected",), ())
        with mock.patch.object(selector.subprocess, "run", return_value=mock.Mock(returncode=0)) as run:
            selector.run_tests(self.root, plan, "unit", "5m")
        self.assertEqual(run.call_args.args[0][-1], "./odd name;echo injected")
        self.assertNotIn("shell", run.call_args.kwargs)


class MetadataTest(unittest.TestCase):
    def test_sequential_json_objects_are_decoded(self):
        items = [{"ImportPath": "a"}, {"ImportPath": "b", "TestImports": ["a"]}]
        text = "\n " + "\n\n".join(json.dumps(item) for item in items) + " \n"
        self.assertEqual(selector.json_objects(text), items)

    def test_invalid_metadata_is_rejected(self):
        for text in ("[]", '{"ImportPath": "a"} broken'):
            with self.subTest(text=text), self.assertRaises(ValueError):
                selector.json_objects(text)


if __name__ == "__main__":
    unittest.main()
