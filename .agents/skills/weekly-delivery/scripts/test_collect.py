import json
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

import collect

STATUS_OPTIONS = ["待评估", "就绪", "进行中", "阻塞", "已完成", "已取消"]


def base_vocabulary(status_options=None):
    """Stand in for the live Base so no test touches the network."""
    options = {
        "状态": status_options or STATUS_OPTIONS,
        "优先级": ["P0", "P1", "P2"],
    }
    return (
        mock.patch.object(collect.feishu, "milestones",
                          return_value={"Eval v1": "rec1", "平台核心持续维护": "rec2"}),
        mock.patch.object(collect.feishu, "select_options",
                          side_effect=lambda table, field: options[field]),
    )


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
        vocab_milestones, vocab_options = base_vocabulary()
        with tempfile.TemporaryDirectory() as tmp, \
             vocab_milestones, vocab_options, \
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

    def run_collect(self, prs, task_rows, state, status_options=None):
        vocab_milestones, vocab_options = base_vocabulary(status_options)
        with tempfile.TemporaryDirectory() as tmp, \
             vocab_milestones, vocab_options, \
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


    def test_draft_carries_the_live_vocabulary(self):
        draft = self.run_collect([], task_rows=[], state="closed")

        self.assertEqual(draft["options"]["里程碑"], ["Eval v1", "平台核心持续维护"])
        self.assertEqual(draft["options"]["状态"], STATUS_OPTIONS)
        self.assertEqual(draft["options"]["优先级"], ["P0", "P1", "P2"])

    def test_a_status_added_in_feishu_counts_as_unfinished(self):
        """A new in-flight stage must not need a script edit to be a zombie."""
        zombie = {
            "_id": "rec9",
            "任务": "交付了但卡在新状态",
            "状态": ["待发布"],
            "完成日期": "2026-08-04T00:00:00.000+08:00",
            "GitHub Issue": "[x](https://github.com/CherryHQ/stella/issues/99)",
        }
        draft = self.run_collect(
            [], task_rows=[zombie], state="closed",
            status_options=STATUS_OPTIONS + ["待发布"],
        )

        self.assertEqual([e["issue"] for e in draft["stale"]], ["99"])

    def test_a_terminal_status_missing_from_the_base_fails_loudly(self):
        with self.assertRaises(SystemExit):
            self.run_collect([], task_rows=[], state="closed",
                             status_options=["待评估", "进行中"])


if __name__ == "__main__":
    unittest.main()
