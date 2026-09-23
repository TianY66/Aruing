# 跨 namespace 同名资源定位修复

本文记录 Aruing Kubernetes 诊断 Agent 中跨 namespace 同名资源消歧的复现方式、实现位置和验证结果。

## 问题场景

测试清单在 `scenarios/same-name-multi-ns/manifests/00-app.yaml` 中创建两个同名 Deployment：

- `team-a/demo-api` 使用可拉取的 `nginx:1.27-alpine` 镜像。
- `team-b/demo-api` 使用不存在的镜像 tag，触发镜像拉取失败。

用户提问「demo-api 起不来是怎么回事」时没有指定 namespace。诊断器不能静默选中其中一个对象并把它当成唯一目标。

## 复现流程

在 WSL/Linux 环境中准备 Docker、kind、kubectl，并在本机配置 LLM（`playground/config.yaml` 或 `ARUING_CONFIG`）。密钥只保存在本机配置中。

手动交互：

```bash
make build
make lab-up NAME=same-name-multi-ns
make lab-chat NAME=same-name-multi-ns
# 输入：demo-api 起不来是怎么回事
# 按提示回复：team-b 的那个
make lab-down NAME=same-name-multi-ns
```

也可以运行该场景的 smoke 流程：

```bash
scripts/smoke-all.sh same-name-multi-ns
```

脚本会创建 kind 集群、运行固定提示词、保存会话日志并清理集群。验收标准见同目录的 `cases/01-default/expect.md` 与 `cases/02-investigate-clarify/expect.md`：Agent 应询问用户或明确呈现歧义；用户选择 `team-b` 后，报告应由该对象的实际工具证据支持，并指出镜像拉取失败。场景脚本负责执行和留痕，期望结果按文档人工检查，不是自动评分。

## 实现要点

- 枚举跨 namespace 的同名资源候选，并以资源名、Kind、API Group 和目标节点识别具体对象。
- 在 `chat` 和 `run` 两条诊断路径中检测歧义；需要用户选择时挂起当前会话。
- 恢复会话后，将用户选择绑定到当前目标，后续查询继续使用所选 namespace 和资源身份，避免错误套用到其他目标。
- 对不同 Kind、不同 API Group、无效答复和多个目标等边界增加回归测试。

## 修改文件

实现：

- `internal/agent/namespace_ambiguity.go`：候选发现、歧义检测和选择绑定逻辑。
- `internal/session/namespace_ambiguity.go`：在对话路径中接入可恢复的澄清挂起。
- `internal/agent/orchestrator.go`、`internal/agent/resolve.go`：解析阶段发现候选、保存用户选择并在恢复后继续定位。
- `internal/agent/tower.go`：聊天工具循环发现跨 namespace 同名对象后触发澄清。
- `internal/tools/tool.go`：增加工具注册状态查询，供诊断路径复用。

回归测试：

- `internal/agent/namespace_ambiguity_test.go`
- `internal/agent/orchestrator_test.go`
- `internal/agent/tower_test.go`

## 验证记录

已在 Linux/WSL 环境完成：

- `go test -race -shuffle=on -count=1 ./...`：通过。
- `go vet ./...`：通过。
- `scripts/smoke-all.sh`：4 类真实 kind 场景通过，其中包含 `same-name-multi-ns` 多轮澄清场景。

复跑相关自动化回归：

```bash
go test -race ./internal/agent ./internal/session -run 'Namespace|DuplicateNamesAcrossNamespaces' -count=1
```

`smoke-all` 日志位于 Git 忽略的 `scenarios/.smoke/`。Kubeconfig 位于 Git 忽略的 `scenarios/.kube/`；不要提交会话数据、集群凭据或本机 LLM 密钥。
