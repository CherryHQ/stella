---
title: Kubernetes
description: 配置和验证原生 Kubernetes sandbox 后端。
---

Stella 不再提供维护中的生产部署 chart。下方原生 Kubernetes sandbox 后端
用于本地开发和测试。生产部署清单由运维自行维护，Stella 仍只支持一个服务副本。

## 原生 Pod sandbox（本地/dev）

设置 `STELLA_SANDBOX_BACKEND=kubernetes`，每个 Session 使用独立 Linux Pod。
首版要求单副本 Stella、同 namespace、同节点及共享
`ReadWriteOnce` PVC。Stella Deployment 使用 `Recreate`。
本地测试清单位于 `test/testbed/kubernetes/fixture.yaml`。
不支持跨节点调度、多副本或自动故障迁移。
目前已验证 Kubernetes 1.35，尚未验证更早版本。
升级曾使用 `STELLA_KUBERNETES_DEPLOYMENT` 的实验部署时，先停止旧 sandbox Pod。
启动清理现在按 PVC UID 分组，不迁移原来的部署标签。

Stella 从默认挂载的 ServiceAccount token 读取 Pod 名称、UID 和 namespace。
主 Pod 保留该 token 挂载即可，不需要 Pod 名称或 namespace 环境变量，
自定义 Pod hostname 也不影响识别。
服务从直接读写挂载到 `/data` 的卷识别 PVC，不使用 `subPath` 或 `subPathExpr`。
`STELLA_HOME` 设为 `/data` 或其子目录。多个容器若在 `/data` 挂载不同 PVC，
启动会报错，不会猜测使用哪个卷。

可选覆盖：

- `STELLA_KUBERNETES_IMAGE`：sandbox Pod 的工具运行环境镜像，与主服务镜像分开。
  发布版默认使用
  `ghcr.io/cherryhq/stella-sandbox:<version>`，开发版使用 `stella-sandbox:dev`。
  自定义镜像须与 stellad 使用相同 builtin bundle；部署时固定 digest。
- `STELLA_SANDBOX_SERVER_URL`：默认使用主 Pod IP 和配置的 HTTP 监听端口，
  端口须为固定非零值。
  代理或自定义路由可覆盖为 sandbox 可达的 HTTP(S) 地址。
  服务须监听 Pod 网络接口，不能只监听回环地址。

服务启动时从 Kubernetes 读取主 Pod UID 和节点，以 PVC UID 关联服务换代前后的
sandbox，并继承主 Pod 的镜像拉取 Secret 引用。PVC 只限定自动清理的存储范围，
与 Docker 按 Stella Home 或数据卷限定清理范围类似；是否为旧执行由 owner Pod 和
启动标识判断，共用 PVC 不代表 sandbox 已失去主人。RBAC 仅需本 namespace 的 Pod create/get/list/delete/patch、
`pods/exec` create，以及指定 PVC 的 get。创建无权限的 `stella-sandbox`
ServiceAccount。sandbox 禁用 token 挂载和 Service 环境注入，使用 UID/GID 1000、
只读镜像根目录、无 capabilities。授权数据目录需要允许该 UID 读写。
每个 sandbox 请求 100m CPU、128Mi 内存，上限为 2 CPU、2Gi 内存、1Gi 临时存储；
启动默认最多等待 120 秒，可用 `STELLA_KUBERNETES_STARTUP_TIMEOUT=2m` 调整。
通过 namespace 配额限制总量。节点还需配置有限的 kubelet `podPidsLimit`，
namespace 配额不能限制 PID 数量。

sandbox 只挂载获授权的 PVC 子目录。builtin 工具来自已校验 bundle revision
的镜像，principal 的 mise 目录保留在 PVC。执行凭据通过 exec stdin 传递，
不写入 Pod 环境变量或 exec URL 参数。

NetworkPolicy 必须默认拒绝 sandbox 网络，使用
`stella.cherryhq.io/network=allow_all` 或 `disabled` 选择模式。
断网模式阻断全部网络，包括 DNS 和 Stella 回调，并移除回调 URL；正常模式仅放行
DNS、Stella 回调端口和获准的公网出口，阻断数据库、集群 API、节点管理和元数据。
必须在实际 CNI 上验证连接，不能把创建策略对象当成策略生效。测试清单阻断全部
IPv6 并限制 IPv4，部署到其他集群时需要调整并重新测试。

超时、exec 断连或未结束进程的 Close 会终止整个 Session Pod，也会中断其中其他
后台进程。执行不会自动重放。服务保留 execution-fence finalizer，直到 Kubernetes
确认终止；API 故障时 Close 可以重试，确认前不创建替代 generation。
runner 关闭失败会保留缓存槽位，阻止该会话重建；删除 owner 也会等待创建中的
runner 返回并关闭。不要通过
force delete 或手动移除 finalizer 绕过节点故障，应先确认进程已停止。Pod 创建
结果不明时暂停后续创建，重启服务后按所属资源恢复清理。显式未知 backend 在
启动时报错。单副本升级有停机，升级前备份数据库和 PVC，镜像回退不会撤销迁移。

本机验证：

```bash
SANDBOX_IMAGE=stella-sandbox:kubernetes-test mise run sandbox:docker:build
mise run test:kubernetes -- --context orbstack --suite all
```

`SANDBOX_IMAGE` 只给本地构建任务的产物命名，不是服务配置。测试清单通过
`STELLA_KUBERNETES_IMAGE` 选择这个 tag，Kubernetes 节点必须能拉取或在本地找到它。

测试任务管理临时 namespace，在受信任的测试 Pod 中运行存储/进程契约，以及
既有 testbed 的 Agent/SSE 链路。`storage`、`process` 可选较窄的契约测试。
常规 `mise run test` 不依赖 K8s。该清单用于测试，不是生产部署清单。
