#!/usr/bin/env python3
"""
简历监控与初筛流程测试脚本
用于测试从 Wiki 问卷同步到候选人汇总表的完整流程
"""

import json
import subprocess
import sys
from typing import Any, Dict, List, Set

# 配置
WIKI_BASE_TOKEN = "N9bvbIrdQarFq0sLjkgcmMVfnTb"
WIKI_TABLE_ID = "tblK0q9RpL0B75rR"
TARGET_BASE_TOKEN = "B7oybOgfZanNdMsPVU3cD6MHnlS"
TARGET_TABLE_ID = "tblozCB8F2NqWiQ4"

# 字段映射
WIKI_FIELDS = {
    "编号": "fldfl4Uosd",
    "姓名": "fldwjCjowp",
    "联系方式": "fldLLvM0QL",
    "目标岗位": "fldAgMUmRw",
    "上传简历": "fldgLm55iV",
    "测试经验": "fldQyHMfo2",
    "自动化测试经验": "fld8tbWYVz",
    "AI产品经验": "fldkzPJ2ct",
    "CherryStudio了解": "fldrHhOyJs",
    "状态": "fldl1CkagH",
}

TARGET_FIELDS = {
    "投递编号": "fldWXQaq4z",
    "候选人姓名": "fldTsxDTka",
    "联系方式": "fldFmSnPqI",
    "应聘岗位": "fldFtOubHr",
    "简历附件": "fldaYhtrZz",
    "来源": "fldnndwBU4",
    "状态": "fldWQAs8HS",
    "综合评分": "fldjdQPI53",
    "备注": "fldQTVGL2f",
}


def run_lark_cli(cmd: List[str]) -> Dict:
    """执行 lark-cli 命令并返回 JSON 结果"""
    full_cmd = ["lark-cli"] + cmd
    result = subprocess.run(full_cmd, capture_output=True, text=True)
    if result.returncode != 0:
        print(f"命令失败: {' '.join(full_cmd)}")
        print(f"错误: {result.stderr}")
        return {"ok": False, "error": result.stderr}
    try:
        return json.loads(result.stdout)
    except json.JSONDecodeError:
        return {"ok": False, "error": "Invalid JSON", "raw": result.stdout}


def get_wiki_records(limit: int = 500) -> List[Dict]:
    """获取 Wiki 问卷中的所有记录"""
    print("📥 正在获取 Wiki 问卷数据...")
    result = run_lark_cli(
        [
            "base",
            "+record-list",
            "--base-token",
            WIKI_BASE_TOKEN,
            "--table-id",
            WIKI_TABLE_ID,
            "--format",
            "json",
            "--limit",
            str(limit),
        ]
    )

    if not result.get("ok"):
        print(f"❌ 获取 Wiki 数据失败: {result}")
        return []

    # 获取字段列表
    fields = result["data"]["fields"]
    field_ids = result["data"]["field_id_list"]
    field_map = dict(zip(field_ids, fields))

    # 解析记录
    records = []
    for i, record_data in enumerate(result["data"]["data"]):
        record = {"_record_id": result["data"]["record_id_list"][i]}
        for field_id, value in zip(field_ids, record_data):
            field_name = field_map.get(field_id, field_id)
            record[field_name] = value
        records.append(record)

    print(f"✅ 获取到 {len(records)} 条 Wiki 记录")
    return records


def get_existing_ids() -> Set[str]:
    """获取目标表格中已有的投递编号"""
    print("📥 正在获取已处理的投递编号...")
    result = run_lark_cli(
        [
            "base",
            "+record-list",
            "--base-token",
            TARGET_BASE_TOKEN,
            "--table-id",
            TARGET_TABLE_ID,
            "--format",
            "json",
            "--field-id",
            "投递编号",
            "--limit",
            "500",
        ]
    )

    if not result.get("ok"):
        print(f"❌ 获取目标表数据失败: {result}")
        return set()

    # 找到投递编号字段的索引
    try:
        field_idx = result["data"]["fields"].index("投递编号")
    except ValueError:
        print("❌ 目标表中没有'投递编号'字段")
        return set()

    existing_ids = set()
    for record in result["data"]["data"]:
        if record[field_idx]:
            existing_ids.add(str(record[field_idx]))

    print(f"✅ 已处理 {len(existing_ids)} 个投递编号")
    return existing_ids


def filter_test_candidates(records: List[Dict]) -> List[Dict]:
    """筛选出应聘测试岗位的候选人"""
    test_candidates = []
    for record in records:
        target = record.get("你的目标岗位")
        if target and isinstance(target, list) and "测试" in str(target):
            test_candidates.append(record)
    return test_candidates


def score_candidate(record: Dict) -> Dict[str, Any]:
    """
    按照全端测试工程师标准进行评分
    满分 100 分
    """
    score_details = {
        "基础背景": 0,  # 20分
        "核心技能": 0,  # 30分
        "加分项": 0,  # 25分
        "综合素质": 0,  # 30分
    }
    highlights = []

    # 1. 基础背景评分 (20分)
    test_exp = record.get("测试经验", [])
    if isinstance(test_exp, list):
        if "中高级经验" in test_exp:
            score_details["基础背景"] += 15
            highlights.append("中高级测试经验")
        elif "初级经验" in test_exp:
            score_details["基础背景"] += 10
            highlights.append("初级测试经验")
        elif "没有正式经验" in test_exp:
            score_details["基础背景"] += 3

    auto_test = record.get("是否有自动化测试经验", [])
    if isinstance(auto_test, list) and "是" in auto_test:
        score_details["基础背景"] += 5
        highlights.append("有自动化测试经验")

    # 2. 核心技能评分 (30分)
    ai_exp = record.get("用过，做过哪些 AI 产品", "")
    if ai_exp and len(str(ai_exp)) > 20:
        score_details["核心技能"] += 15
        highlights.append("AI产品使用经验")

    cs_know = record.get("对 Cherry Studio 的了解程度", [])
    if isinstance(cs_know, list):
        if "熟悉" in cs_know:
            score_details["核心技能"] += 10
            highlights.append("熟悉Cherry Studio")
        elif "一般" in cs_know:
            score_details["核心技能"] += 5

    # 3. 加分项评分 (25分)
    open_src = record.get("是否有开源项目？或者参与已上线的项目，请填写相关地址", "")
    if open_src and len(str(open_src)) > 10:
        score_details["加分项"] += 10
        highlights.append("有开源/上线项目")

    # AI 相关加分
    if ai_exp and (
        "cursor" in str(ai_exp).lower()
        or "claude" in str(ai_exp).lower()
        or "agent" in str(ai_exp).lower()
    ):
        score_details["加分项"] += 10
        highlights.append("熟练使用AI工具")

    # 4. 综合素质评分 (30分)
    words = record.get("有哪些想对我们说的话？", "")
    if words and len(str(words)) > 50:
        score_details["综合素质"] += 20
        highlights.append("主动表达意愿强")
    elif words and len(str(words)) > 10:
        score_details["综合素质"] += 10

    # 限制各项不超过满分
    score_details["基础背景"] = min(score_details["基础背景"], 20)
    score_details["核心技能"] = min(score_details["核心技能"], 30)
    score_details["加分项"] = min(score_details["加分项"], 25)
    score_details["综合素质"] = min(score_details["综合素质"], 30)

    total_score = sum(score_details.values())

    return {
        "score": total_score,
        "score_details": score_details,
        "highlights": "、".join(highlights[:5]) if highlights else "暂无亮点",
    }


def download_resume(file_token: str, output_path: str) -> bool:
    """下载简历附件"""
    result = run_lark_cli(
        [
            "drive",
            "+media-download",
            "--file-token",
            file_token,
            "--output",
            output_path,
        ]
    )
    return result.get("ok", False)


def create_candidate_record(candidate: Dict, score_info: Dict) -> bool:
    """在目标表格中创建新记录"""
    # 准备字段值 - 按照 lark-cli 要求的格式
    name = str(candidate.get("你的姓名", ""))
    contact = str(candidate.get("你的联系方式", ""))

    # 构建 JSON 格式：{"fields": [字段名列表], "rows": [值列表]}
    batch_data = {
        "fields": [
            "投递编号",
            "候选人姓名",
            "联系方式",
            "应聘岗位",
            "来源",
            "状态",
            "综合评分",
            "基础背景评分 (20)",
            "核心技能评分 (30)",
            "加分项评分 (25)",
            "综合素质评分 (30)",
            "备注",
        ],
        "rows": [
            [
                str(candidate.get("编号", "")),
                name,
                contact if contact else "未提供",
                "全端测试工程师",
                "Wiki 推荐",
                "初筛中",
                score_info["score"],
                score_info["score_details"]["基础背景"],
                score_info["score_details"]["核心技能"],
                score_info["score_details"]["加分项"],
                score_info["score_details"]["综合素质"],
                score_info["highlights"],
            ]
        ],
    }

    json_str = json.dumps(batch_data)

    result = run_lark_cli(
        [
            "base",
            "+record-batch-create",
            "--base-token",
            TARGET_BASE_TOKEN,
            "--table-id",
            TARGET_TABLE_ID,
            "--json",
            json_str,
        ]
    )

    return result.get("ok", False)


def process_new_candidates():
    """主流程：处理新的候选人"""
    print("=" * 60)
    print("🚀 简历监控与初筛流程测试")
    print("=" * 60)

    # 1. 获取 Wiki 数据
    wiki_records = get_wiki_records()
    if not wiki_records:
        print("❌ 没有获取到 Wiki 数据")
        return

    # 2. 获取已处理的编号
    existing_ids = get_existing_ids()

    # 3. 筛选测试岗位候选人
    test_candidates = filter_test_candidates(wiki_records)
    print(f"📊 筛选出 {len(test_candidates)} 个测试岗位候选人")

    # 4. 找出新候选人
    new_candidates = []
    for candidate in test_candidates:
        candidate_id = str(candidate.get("编号", ""))
        if candidate_id and candidate_id not in existing_ids:
            new_candidates.append(candidate)

    print(f"🆕 发现 {len(new_candidates)} 个新候选人")

    if not new_candidates:
        print("✅ 没有新候选人需要处理")
        return

    # 5. 处理新候选人
    processed = 0
    for candidate in new_candidates[:3]:  # 只处理前3个用于测试
        candidate_id = str(candidate.get("编号", ""))
        name = str(candidate.get("你的姓名", "未知"))

        print(f"\n📋 处理候选人: {name} (编号: {candidate_id})")

        # 评分
        score_info = score_candidate(candidate)
        print(f"   评分: {score_info['score']} 分")
        print(f"   亮点: {score_info['highlights']}")

        # 创建记录（跳过简历下载步骤）
        success = create_candidate_record(candidate, score_info)
        if success:
            processed += 1
            print(f"   ✅ 成功创建记录")
        else:
            print(f"   ❌ 创建记录失败")

    print(f"\n🎉 测试完成，成功处理 {processed} 个候选人")


def analyze_flow_issues():
    """分析流程中可能存在的问题和优化点"""
    print("\n" + "=" * 60)
    print("🔍 流程分析和优化建议")
    print("=" * 60)

    issues = []

    # 1. 检查数据获取
    wiki_records = get_wiki_records(limit=5)
    if wiki_records:
        sample = wiki_records[0]
        issues.append(
            {
                "category": "数据获取",
                "issue": "Wiki 数据获取成功，但分页处理需要优化"
                if len(wiki_records) >= 5
                else "数据量较少",
                "suggestion": "如果候选人很多，需要实现分页拉取全部数据",
            }
        )

    # 2. 检查字段映射
    target_result = run_lark_cli(
        [
            "base",
            "+table-get",
            "--base-token",
            TARGET_BASE_TOKEN,
            "--table-id",
            TARGET_TABLE_ID,
        ]
    )

    if target_result.get("ok"):
        target_fields = {f["name"]: f["id"] for f in target_result["data"]["fields"]}
        required_fields = ["投递编号", "候选人姓名", "简历附件", "综合评分"]
        missing = [f for f in required_fields if f not in target_fields]
        if missing:
            issues.append(
                {
                    "category": "字段映射",
                    "issue": f"目标表缺少字段: {missing}",
                    "suggestion": "需要在目标表格中创建缺失字段",
                }
            )

    # 3. 检查重复检测
    existing_ids = get_existing_ids()
    issues.append(
        {
            "category": "重复检测",
            "issue": f"当前已处理 {len(existing_ids)} 条记录",
            "suggestion": "建议增加增量同步机制，只查询最近 N 天的记录提高效率",
        }
    )

    # 4. 评分逻辑
    issues.append(
        {
            "category": "评分逻辑",
            "issue": "当前评分为简单规则匹配，可能不够精准",
            "suggestion": "建议结合简历内容进行 LLM 辅助评分",
        }
    )

    # 5. 简历下载
    issues.append(
        {
            "category": "简历处理",
            "issue": "需要下载简历附件并上传到目标表",
            "suggestion": "使用 lark-cli drive +media-download 下载，再用 base +record-upload-attachment 上传",
        }
    )

    # 6. 通知机制
    issues.append(
        {
            "category": "通知机制",
            "issue": "任务描述要求发送通知，但流程中未实现",
            "suggestion": "可以使用 notify 工具或飞书消息 API 发送通知",
        }
    )

    # 输出分析结果
    for i, issue in enumerate(issues, 1):
        print(f"\n{i}. 【{issue['category']}】")
        print(f"   问题: {issue['issue']}")
        print(f"   建议: {issue['suggestion']}")

    return issues


if __name__ == "__main__":
    if len(sys.argv) > 1 and sys.argv[1] == "analyze":
        analyze_flow_issues()
    else:
        process_new_candidates()
        analyze_flow_issues()
