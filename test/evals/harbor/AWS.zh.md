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

## 性能实验

默认仍使用原来的模型 warm-up。显式传入 `--warmup environment` 后，
改用 Harbor 的 `nop --install-only` 启动并检查固定版本的任务环境，
不调用模型、不运行 verifier。环境准备最多并行 4 个，保留 Docker 构建产物。
它不验证 Stella 安装和模型连通性，这些仍由后续真实任务验证；
缓存是否复用、能快多少，必须在工作机器上测量。

`--topup-concurrency 4` 将缺少证据的任务放进同一个并行队列，
不会超过主跑的并发上限。只补跑无效或缺少评分的尝试，不会重试可计分的零分。
默认值 1 保留逐个补跑。

选择新的并发前，可以运行有界 AWS pilot：

```bash
mise run eval:tb21:aws -- --pilot --warmup environment --concurrency 4 --topup-concurrency 4 --timeout-hours 3 --commit HEAD
```

该实验先准备全部 89 个环境，再在同一台临时机器上将固定的 5 个 smoke
任务跑四轮，并发依次为 `1,4,4,1`。这是性能实验，不是完整 benchmark，
也不支持模型能力提升的结论。任务 deadline 不变，混合样本不记录单一并发指纹。
`performance.json` 保存各阶段耗时、退出码及采样的主机内存、CPU、负载，
不记录命令参数和凭据；补跑时间与主跑分开。仍使用原有资源清理与强制关机兜底。

预热方式或并发改变后，评估条件也改变。不能把实验成绩与历史基线当作
条件相同的结果比较。

控制器把进度写入 `dist/evals/aws/<run-id>/journal.ndjson`。
stdout 管道关闭不会中断评估或文件日志。即使失败报告本身抛出异常，
控制器也会尝试清理资源。如果清理被中断，用以下命令恢复：

```bash
mise run eval:tb21:aws -- --cleanup dist/evals/aws/<run-id>
```
