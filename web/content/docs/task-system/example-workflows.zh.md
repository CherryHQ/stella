---
title: 示例工作流
---

这些例子展示 Stella 的任务系统如何把 goal 变成经过 review 的工作。

## 财务报销 review

Goal：

> 审计这份客户晚餐报销材料；如有需要，准备财务 review。

可能的 tasks：

- 提取发票和参与人信息。
- 检查制度要求。
- 识别缺失字段。
- 标记制度例外。
- 起草报销材料包。
- 创建财务 review task。

Review gates：

- 制度例外需要财务 review。
- 缺少税务或票据信息时需要用户补充。
- 付款审批仍由财务人员负责。

## HR 招聘流程

Goal：

> 根据后端岗位筛选这些候选人，并准备 hiring panel review。

可能的 tasks：

- 提取简历事实。
- 根据岗位 rubric 对比候选人。
- 总结证据。
- 识别缺失的面试信号。
- 准备 panel review packet。
- 安排 review 或通知 hiring panel。

Review gates：

- 候选人推荐需要人工 review。
- 敏感或不完整证据会被标记。
- 最终录用决定不交给 Agent。

## 工程发布

Goal：

> 规划 billing workflow 发布，并追踪上线前所有 blocker。

可能的 tasks：

- 阅读发布计划。
- 识别受影响服务。
- 创建实现和验证 tasks。
- 添加验收标准。
- 追踪 blockers。
- 准备发布 review。

Review gates：

- 高风险 migration 需要工程 review。
- 面向客户的变化需要产品 review。
- 上线批准仍由人负责。
