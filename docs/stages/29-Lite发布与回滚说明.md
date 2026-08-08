# 阶段 29：Lite 发布与回滚说明

## 目标与不变量

本阶段为 Lite MVP 补齐发布层面的可复核说明：多架构构建/SBOM 流程、版本矩阵、digest 记录、升级和
回滚。它不修改 Go 核心逻辑，也不启动 Standard mode。

1. mutable tag 不是发布身份；release record 必须包含 Git commit、manifest/per-arch image digest、
   SBOM digest 和构建器版本。
2. 当前仅 Linux amd64 离线导入路径在 GPU 节点实证。arm64 仅为 Dockerfile 的交叉构建 target；没有
   未经验证就宣称多架构镜像、SBOM attestation 或 registry release 已完成。
3. Gateway 升级遵守 PDB 和 rolling update；vLLM 不得并行重启两个 time-sliced 副本。
4. exact 信号 unavailable/stale 的恢复仍是 load-aware；回滚不能将它误写为零命中或带着无效 index
   继续 exact。
5. Flannel 不保证 NetworkPolicy 执行，升级流程不依赖此声明。

## 交付

`docs/notes/release-notes.md` 给出已验证离线路径、发布前 multi-arch/SBOM 命令、版本矩阵和 digest
记录要求；同时给出 Gateway `rollout undo` 与 exact 链路异常时恢复 `deploy/baseline/base` 的最小步骤。
`syft`、可写 registry 和远程 attestation 是外部发布条件，缺少任一项时必须标记为未完整发布。

## 验证

本阶段仅新增文档，执行：

```bash
make ci
git diff --check
```

Lite 产品化收尾至此完成；下一项需要真实 registry/arm64/监控环境时，应作为独立发布验收，不与
R6E Standard mode 混合。
