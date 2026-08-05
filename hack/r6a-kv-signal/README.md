# R6A KV 信号探针

这是阶段 18 的一次性工程探针，不是 FishMesh 产品二进制。它复用固定版本的
`llm-d-kv-cache` 完成 vLLM event 解析、block key 重建、索引和 longest-prefix score；只补充
上游当前没有提供的 replay 心跳与序列状态观察。

探针提供五个本地控制端点：

- `GET /state`：逐 Pod event/replay/freshness 与进程堆内存；
- `POST /generate`：把请求定向发给某个真实 vLLM Pod；
- `POST /score`：调用该 Pod 的 Render API，并查询所有候选的最长缓存前缀；
- `POST /stream`：暂停/恢复某个订阅，用于验证 replay；
- `POST /clear`：模拟 Pod lifecycle owner 的删除通知并清理该 Pod 索引。

Linux amd64 构建示例：

```bash
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o /tmp/r6a-kv-signal \
  ./hack/r6a-kv-signal
```

`--backend` 可重复，格式是：

```text
<topic中的Pod IP:8000>,http://<Pod IP>:8000,tcp://<Pod IP>:5557,tcp://<Pod IP>:5558
```

探针默认只监听 `127.0.0.1:19090`，不会对集群或公网暴露控制接口。完整操作、输入和实测结果
记录在阶段 18 文档中。
