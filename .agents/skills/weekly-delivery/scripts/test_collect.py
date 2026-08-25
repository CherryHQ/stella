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


    def test_closed_issue_flips_a_lingering_status(self):
        pr = {
            "number": 12,
            "title": "the work",
            "url": "https://example.test/pr/12",
            "createdAt": "2026-08-12T00:00:00Z",
            "mergedAt": "2026-08-13T00:00:00Z",
            "body": "Closes #34",
        }
        task = {
            "_id": "rec1",
            "任务": "既有任务",
            "状态": ["进行中"],
            "GitHub Issue": "[x](https://github.com/CherryHQ/stella/issues/34)",
        }
        draft = self.run_collect([pr], task_rows=[task], state="closed")

        self.assertEqual(draft["update"][0]["状态"], "已完成")
        self.assertEqual(draft["stale"], [])

    def test_open_issue_keeps_its_status(self):
        pr = {
            "number": 12,
            "title": "the work",
            "url": "https://example.test/pr/12",
            "createdAt": "2026-08-12T00:00:00Z",
            "mergedAt": "2026-08-13T00:00:00Z",
            "body": "Closes #34",
        }
        task = {
            "_id": "rec1",
            "任务": "既有任务",
            "状态": ["进行中"],
            "GitHub Issue": "[x](https://github.com/CherryHQ/stella/issues/34)",
        }
        draft = self.run_collect([pr], task_rows=[task], state="open")

        self.assertNotIn("状态", draft["update"][0])

    def test_earlier_week_zombie_is_reported_as_stale(self):
        pr = {
            "number": 12,
            "title": "the work",
            "url": "https://example.test/pr/12",
            "createdAt": "2026-08-12T00:00:00Z",
            "mergedAt": "2026-08-13T00:00:00Z",
            "body": "Closes #34",
        }
        zombie = {
            "_id": "rec9",
            "任务": "上上周就交付了",
            "状态": ["进行中"],
            "完成日期": "2026-08-04T00:00:00.000+08:00",
            "GitHub Issue": "[x](https://github.com/CherryHQ/stella/issues/99)",
        }
        untouched = {
            "_id": "rec8",
            "任务": "还没开始",
            "状态": ["就绪"],
            "GitHub Issue": "[x](https://github.com/CherryHQ/stella/issues/98)",
        }
        draft = self.run_collect([pr], task_rows=[zombie, untouched], state="closed")

        # rec8 has no 完成日期, so it is not a zombie and costs no API call.
        self.assertEqual(
            draft["stale"],
            [{"issue": "99", "record_id": "rec9", "task_title": "上上周就交付了",
              "was": "进行中", "状态": "已完成"}],
        )

    def run_collect(self, prs, task_rows, state):
        with tempfile.TemporaryDirectory() as tmp, \
             mock.patch.object(collect, "merged_prs", return_value=prs), \
             mock.patch.object(collect, "is_issue", return_value=True), \
             mock.patch.object(collect, "issue_meta", return_value={
                 "title": "some work", "state": state, "labels": [],
             }), \
             mock.patch.object(collect, "feishu_tasks", return_value=task_rows), \
             mock.patch.object(sys, "argv", ["collect.py", "--week-start", "2026-08-11",
                                             "--out", str(Path(tmp) / "draft.json")]):
            collect.main()
            return json.loads((Path(tmp) / "draft.json").read_text())


    def test_new_entry_carries_the_current_judgement_fields(self):
        pr = {
            "number": 12,
            "title": "the work",
            "url": "https://example.test/pr/12",
            "createdAt": "2026-08-12T00:00:00Z",
            "mergedAt": "2026-08-13T00:00:00Z",
            "body": "Closes #34",
        }
        draft = self.run_collect([pr], task_rows=[], state="closed")

        entry = draft["new"][0]
        self.assertEqual(
            {k for k, v in entry.items() if v is None},
            {"任务", "状态", "优先级", "里程碑", "描述"},
        )


if __name__ == "__main__":
    unittest.main()
