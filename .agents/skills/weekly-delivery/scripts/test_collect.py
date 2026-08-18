import json
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

import collect


class CollectTest(unittest.TestCase):
    def test_release_skip_remains_visible_in_draft(self):
        pr = {
            "number": 12,
            "title": "release work",
            "url": "https://example.test/pr/12",
            "createdAt": "2026-08-12T00:00:00Z",
            "mergedAt": "2026-08-13T00:00:00Z",
            "body": "Closes #34",
        }
        with tempfile.TemporaryDirectory() as tmp, \
             mock.patch.object(collect, "merged_prs", return_value=[pr]), \
             mock.patch.object(collect, "is_issue", return_value=True), \
             mock.patch.object(collect, "issue_meta", return_value={
                 "title": "release: validate candidate artifacts",
                 "state": "CLOSED",
                 "labels": [],
             }), \
             mock.patch.object(collect, "feishu_tasks", return_value=[]), \
             mock.patch.object(sys, "argv", ["collect.py", "--week-start", "2026-08-11", "--out", str(Path(tmp) / "draft.json")]):
            collect.main()
            draft = json.loads((Path(tmp) / "draft.json").read_text())

        self.assertEqual(draft["new"], [])
        self.assertEqual(draft["update"], [])
        self.assertEqual(draft["stats"]["skipped_release_issues"][0]["issue"], "34")


if __name__ == "__main__":
    unittest.main()
