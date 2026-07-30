# Backend Snapshot 观测实验

这个 overlay 叠加在 `endpoint-slice` 实验之上，开启 Gateway 对每个 EndpointSlice
地址的 vLLM `/metrics` 采集。采集结果不会改变当前 Prefix Affinity 决策，只会写入
Gateway `/metrics`，用于验证 backend ID、queue、running、Prefix Cache 和 freshness 是否
与动态地址正确对应。

GPU exporter 和 Kubernetes Events 仍是节点/namespace 级信号，本阶段不把它们错误地复制
到每个 Pod；等建立明确的 Pod/Node 身份映射后再进入 Hybrid Scheduler 输入。
