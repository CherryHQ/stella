# AWS 评估配置

[English](AWS.md)

AWS runner 从部署本地的 `.env` 读取 `AWS_REGION`、`OPENAI_BASE_URL`、
`OPENAI_API_KEY` 和 `OPENAI_MODEL`，支持使用 Luna 以外的模型。
不要把凭据提交到 Git 或输出到日志。

在 `.env` 或调用环境中设置全部四项价格，单位为美元/百万 token：
`EVAL_COST_INPUT`、`EVAL_COST_OUTPUT`、`EVAL_COST_CACHE_READ` 和
`EVAL_COST_CACHE_WRITE`。不单独收费的类别填写 `0`。
runner 会把这些值传到远端评估；它们是手动提供的估价，不是从网关查询的
实际价格或账单对账结果。`.env` 中的值会覆盖调用环境中的同名变量。

```bash
mise run eval:tb21:aws -- --plan
mise run eval:tb21:aws -- --smoke --commit HEAD
mise run eval:tb21:aws -- --commit HEAD
```

Full 模式先运行一轮不计成绩的 warm-up，再按顺序运行五轮完整数据集。
warm-up 中缺少可计分证据的任务会自动补跑，最多补跑
`--max-topup-rounds` 轮，默认 3 轮；补跑不根据 reward 筛选。
正式评估前会丢弃 warm-up 证据。新运行 ID 使用 `tb21-experimental` 前缀，
引用成绩时应记录模型和 commit。不同模型是独立实验，不构成相同模型下
与 Luna 基线的比较；比较结论遵循 [PROTOCOL.md](PROTOCOL.md)。

控制器把进度写入 `dist/evals/aws/<run-id>/journal.ndjson`。
stdout 管道关闭不会中断评估或文件日志。即使失败报告本身抛出异常，
控制器也会尝试清理资源。如果清理被中断，用以下命令恢复：

```bash
mise run eval:tb21:aws -- --cleanup dist/evals/aws/<run-id>
```
