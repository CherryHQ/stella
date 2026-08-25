import json
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

import write

LIVE = {
    "状态": ["待评估", "就绪", "进行中", "阻塞", "已完成", "已取消"],
    "优先级": ["P0", "P1", "P2"],
}
MILESTONES = {"Eval v1": "recEval", "平台核心持续维护": "recCore"}


def entry(**over):
    base = {
        "issue": "34",
        "issue_url": "https://github.com/CherryHQ/stella/issues/34",
        "issue_state": "closed",
        "last_merged": "2026-08-20",
        "pr_field": "[#12](https://github.com/CherryHQ/stella/pull/12)",
        "任务": "做完了",
        "状态": "已完成",
        "优先级": "P1",
        "描述": "验收：能跑通",
        "里程碑": "Eval v1",
    }
    base.update(over)
    return base


class MilestoneCellTest(unittest.TestCase):
    def test_a_known_name_becomes_a_link_cell(self):
        self.assertEqual(write.milestone_cell("Eval v1", MILESTONES), [{"id": "recEval"}])

    def test_an_empty_name_stays_empty(self):
        self.assertIsNone(write.milestone_cell(None, MILESTONES))

    def test_an_unknown_name_stops_the_write(self):
        with self.assertRaises(SystemExit):
            write.milestone_cell("没这个里程碑", MILESTONES)


class DryRunTest(unittest.TestCase):
    def run_write(self, draft):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "draft.json"
            path.write_text(json.dumps(draft, ensure_ascii=False))
            with mock.patch.object(write.feishu, "milestones", return_value=MILESTONES), \
                 mock.patch.object(write.feishu, "select_options",
                                   side_effect=lambda table, field: LIVE[field]), \
                 mock.patch.object(sys, "argv",
                                   ["write.py", "--draft", str(path), "--dry-run"]):
                write.main()

    def test_a_milestone_added_in_feishu_needs_no_script_edit(self):
        """The map is fetched, so a name the Base knows is accepted as is."""
        with mock.patch.object(write.feishu, "milestones",
                               return_value={**MILESTONES, "Eval v2": "recEval2"}), \
             mock.patch.object(write.feishu, "select_options",
                               side_effect=lambda table, field: LIVE[field]):
            self.assertEqual(
                write.milestone_cell("Eval v2", write.feishu.milestones()),
                [{"id": "recEval2"}],
            )

    def test_a_status_the_base_does_not_offer_stops_the_write(self):
        draft = {"new": [entry(状态="已归档")], "update": [], "stale": []}
        with self.assertRaises(SystemExit):
            self.run_write(draft)

    def test_a_complete_draft_passes_validation(self):
        draft = {"new": [entry()], "update": [], "stale": []}
        self.run_write(draft)


if __name__ == "__main__":
    unittest.main()
