# 泄洪闸门操作协调服务

这是一个无外部依赖的 Go 后端，用本地内存仓库协调闸门登记、开度调整命令、安全联锁复核和执行结果。服务强调单闸门活动命令互斥、原子状态转换、执行令牌生命周期和稳定错误传播。

## 运行

```sh
go build ./...
go run ./cmd/server --addr 127.0.0.1:18119
```

健康入口为 `GET http://127.0.0.1:18119/health`。

## API

```text
POST /gates
GET  /gates/{id}
POST /commands
GET  /commands/{id}
PUT  /commands/{id}
POST /commands/{id}/interlock
POST /commands/{id}/execute
```

登记闸门示例：

```json
{
  "id": "gate-east",
  "name": "东侧泄洪闸",
  "max_aperture": 4.5,
  "initial_aperture": 0.5
}
```

提交命令示例：

```json
{
  "gate_id": "gate-east",
  "target_aperture": 2.25,
  "reason": "上游水位调整",
  "requested_by": "dispatcher-lin"
}
```

安全复核前，调度员可以替换待复核命令的业务事实：

```json
{
  "target_aperture": 2.6,
  "reason": "最新来水预报调整",
  "requested_by": "dispatcher-lin",
  "expected_version": 1
}
```

替换成功后命令 ID 和起始开度保持不变，版本号递增；旧目标开度、申请依据、版本和被替换时间保留在 `revisions` 中。后续复核只处理当前快照，旧内容不能再被独立复核。`expected_version` 必须等于读取到的当前版本；重复提交、复核抢先或命令已进入其他状态都会返回冲突且不改变原申请。

联锁复核示例：

```json
{
  "verdict": "clear",
  "verifier": "safety-wu",
  "note": "上下游条件满足"
}
```

执行示例：

```json
{
  "operator": "operator-chen",
  "token": "复核响应中的 execution_token",
  "simulate_fault": false
}
```

## 目录

- `cmd/server`：服务进程。
- `cmd/workflowcheck`：四条工作流的真实 HTTP 检查入口。
- `internal/bootstrap`：依赖装配。
- `internal/contracts`：公开数据契约。
- `internal/coordination`：业务工作流。
- `internal/domain`：实体、不变量和状态机。
- `internal/httpapi`：路由、解码和错误映射。
- `internal/storage`：互斥保护的内存仓库。

## 工作流检查

```sh
go run ./cmd/workflowcheck --workflow register-gate
go run ./cmd/workflowcheck --workflow request-gate-command
go run ./cmd/workflowcheck --workflow replace-gate-command
go run ./cmd/workflowcheck --workflow review-safety-interlock
go run ./cmd/workflowcheck --workflow execute-gate-command
```

这些检查会创建临时 HTTP 服务并调用真实路由。它们不调用外部网络，也不依赖数据库。

## 测试

`cmd/workflowcheck` 通过临时真实 HTTP 服务覆盖五条工作流；替换流程的领域规则、并发胜负和 HTTP 持久化结果另有 Go 测试覆盖。
