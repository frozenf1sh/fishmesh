# 阶段 28：Lite 五分钟 Demo 与完成度对齐

## 目标与契约

README 和 `README_CN.md` 是 Lite MVP 的最短操作入口，必须与仓库和真实集群的已完成边界一致：

1. 默认部署仍是 `bounded-affinity`；exact 只由 `deploy/lite-exact` 显式开启，演示结束必须恢复
   baseline。
2. exact 演示使用两个不带 `X-FishMesh-Prefix-Key` 的请求，共用一段足以跨多个 KV block 的 system
   prompt、使用不同 user message，并检查决策头而不是推断缓存状态。
3. `match-unavailable`/`exact-load-fallback-v1` 是 replay 未确认时的正确 load-aware 降级；
   `available` 且 cached tokens 为 0 才是可参与 exact 的真实零命中。
4. R6E Standard mode/llm-d 部署后置，不把已有 adapter 当成已经完成的产品安装面。

## 实际完成度

R6A–R6C 的 Lite exact 数据面和 R6D/D2 的有限 profile 已完成；README 的路线图相应收口。
仓库中也已有 dashboard、Prometheus rule 文件和 runbook，但参考集群没有 Prometheus、Grafana 或
monitoring operator CRD，故 dashboard 导入、metrics 查询和 alert delivery 均未实集群验证。README
显式保留这一边界，不能将这些配置写为“已交付运行中的监控服务”。

Flannel 下 NetworkPolicy 是否执行由 CNI 决定；演示没有把 NetworkPolicy 作为安全或可用性前提。

## 验证

本阶段只修改文档，没有改动 Go 核心逻辑。执行：

```bash
make ci
git diff --check
```

下一阶段补齐多架构、SBOM、版本矩阵以及升级/回滚说明；仍不启动 R6E。
