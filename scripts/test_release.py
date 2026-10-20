import os
from pathlib import Path
import subprocess
import tempfile
import unittest

from release import plan


class ReleaseTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.cwd = os.getcwd()
        os.chdir(self.temp.name)
        self.git("init", "-q")
        self.git("config", "user.email", "test@example.invalid")
        self.git("config", "user.name", "Test")

    def tearDown(self):
        os.chdir(self.cwd)
        self.temp.cleanup()

    def git(self, *args):
        return subprocess.check_output(["git", *args], text=True).strip()

    def commit(self, text):
        self.git("commit", "--allow-empty", "-qm", text)

    def base(self, name):
        self.git("tag", name)
        return [{"tagName": name, "isDraft": False, "isPrerelease": False}]

    def test_first_minor_and_feature_dominates_fix(self):
        self.commit("feat: first feature")
        self.commit("fix: later fix")
        self.assertEqual(plan([])["version"], "v0.1.0")

    def test_release_baseline_noop_and_patch(self):
        self.commit("feat: already shipped")
        base = self.base("v1.2.3")
        self.commit("docs: instructions")
        self.assertFalse(plan(base)["release"])
        self.commit("fix(cli): handle paths with spaces")
        self.assertEqual(plan(base)["version"], "v1.2.4")

    def test_breaking_change_footer_and_exclamation(self):
        for message in ["feat(api)!: new contract", "fix: new contract\n\nBREAKING CHANGE: removed old endpoint"]:
            with self.subTest(message=message):
                self.commit(message)
                self.assertEqual(plan([])["version"], "v1.0.0")

    def test_drafts_and_prereleases_are_not_baselines(self):
        self.commit("feat: new feature")
        releases = [{"tagName": "v9.0.0", "isDraft": True, "isPrerelease": False},
                    {"tagName": "v2.0.0-rc1", "isDraft": False, "isPrerelease": True}]
        self.assertEqual(plan(releases)["version"], "v0.1.0")

    def test_old_workflow_does_not_overwrite_new_release(self):
        self.commit("feat: old")
        old = self.git("rev-parse", "HEAD")
        self.commit("feat: new")
        base = self.base("v0.2.0")
        self.git("checkout", "--detach", "-q", old)
        self.assertFalse(plan(base)["release"])

    def test_commit_text_stays_data(self):
        self.commit("fix: $(touch stolen) `touch stolen`")
        self.assertIn("$(touch stolen)", plan([])["notes"])
        self.assertFalse(Path("stolen").exists())
