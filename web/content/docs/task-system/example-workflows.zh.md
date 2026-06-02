---
title: 示例工作流
---

这些例子展示如何把较大的 goal 显式建模成 tasks。本版本不包含自动子任务规划；请自己创建 tasks，或让 Agent 通过任务命令创建它们。

## 财务报销 review

Goal：

> 审计这份客户晚餐报销材料；如有需要，准备财务 review。

创建子 tasks：

- 提取发票和参与人信息。
- 检查制度要求。
- 识别缺失字段。
- 标记制度例外。
- 起草报销材料包。
- 为例外项创建人工财务 review task。

有用的依赖：

- 制度检查依赖票据信息提取。
- 例外 review 依赖制度检查。
- 最终材料包依赖提取结果和例外 review。

Review gates：

- 制度例外需要人工财务 review。
- 缺少税务或票据信息时需要用户补充。
- 付款审批仍由财务人员负责。

## HR 招聘流程

Goal：

> 根据后端岗位筛选这些候选人，并准备 hiring panel review。

创建子 tasks：

- 提取简历事实。
- 根据岗位 rubric 对比候选人。
- 总结证据。
- 识别缺失的面试信号。
- 准备 panel review packet。
- 通知 hiring panel 进行人工 review。

有用的依赖：

- Rubric 对比依赖简历事实提取。
- 证据总结依赖 rubric 对比。
- Panel packet 依赖总结和缺失信号检查。

Review gates：

- 候选人推荐需要人工 review。
- 敏感或不完整证据会被标记。
- 最终录用决定不交给 Agent。

## 工程发布

Goal：

> 规划 billing workflow 发布，并追踪上线前所有 blocker。

创建子 tasks：

- 阅读发布计划。
- 识别受影响服务。
- 创建实现 tasks。
- 创建验证 tasks。
- 追踪 blockers。
- 准备发布 review。

有用的依赖：

- 实现 tasks 依赖受影响服务分析。
- 验证 tasks 依赖实现 tasks。
- 发布 review 依赖验证结果。

Review gates：

- 高风险 migration 需要工程 review。
- 面向客户的变化需要产品 review。
- 上线批准仍由人负责。
