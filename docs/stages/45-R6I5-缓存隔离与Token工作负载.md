# 阶段 45：R6I-5 缓存隔离与 Token 工作负载

## 1. 范围

本阶段把 benchmark 从“只声明 prefix bytes”升级为可执行、可审计的 cache generation 与实际 prompt token
协议。它仍不运行 GPU 实验；真实系数和性能结论留给 R6I-6。

## 2. Cache 隔离

每个 isolated run 必须声明 `workload_seed`、`treatment`、唯一 `run_nonce` 和实际 vLLM
`cache_generation`。客户端只把这些值与有限场景身份做 SHA-256 派生，再把 64 字符 `cache_salt` 放进
OpenAI 请求体：

- `cold`：每个正式请求使用不同 salt，禁止 warmup；
- `controlled-warm`：同一 scenario/prefix group 的 warmup 与正式请求复用 salt；warmup 单独写成
  `record_type=warmup`，不进入正式统计，并要求 backend provenance 存在且同组不漂移；
- `steady-warm`：run 内同一 prefix group 自然复用，允许但不要求显式 warmup。

逐请求产物只记录 `cache_mode`、`cache_scope` 和 `cache_generation`，不记录派生 salt。paired treatment
复用相同 workload seed，但必须覆盖为不同 run nonce，因而不会命中上一 treatment 的 namespace。

## 3. Token 目标

场景可同时声明 `target_prompt_tokens`、`prompt_token_tolerance` 和生成用 `prefix_bytes`。正式值以 Gateway
返回的 `X-FishMesh-Prompt-Tokens` 为准；report 输出实际 min/P50/P95/max、缺失数和越界数。任何目标场景
出现 token evidence 缺失或超出 tolerance 时，raw/report 仍落盘，但命令以失败结束，不能把 byte 长度改称
token 结果。

仓库新增 `configs/token-ladder-isolated.json`，提供 512/1024/2048/3072 token 的受控 warm 模板。
其中 byte 值只是首次 Render 校准的起点，真实运行前必须替换 treatment、run nonce 和 cache generation。

## 4. 顺序与 provenance

- `workload_seed` 确定性打乱 scenario，并把最终 `execution_order` 写入 `plan.json`；
- `formal=true` 时强制填写 Git SHA、digest-pinned Gateway image、Gateway Pod UID、vLLM/model、配置 digest
  和 estimator profile；
- CLI 允许逐轮覆盖 run ID、treatment、seed、nonce 和 cache generation；
- metadata、warmup、request、report 共用一个 JSONL，compare 只读取 `record_type=request`。

## 5. Profile 边界修正

static profile 新增独立 `max_prompt_tokens`：`max_model_tokens=4096` 表示运行时容量，
`max_prompt_tokens=3072` 表示本轮已校准范围。超过 3072 但未超过模型容量的请求仍返回 typed
`out-of-range`，不能用未测区间外推后标记 calibrated。旧 profile 未声明该字段时兼容地使用
`max_model_tokens`。

## 6. 验证与下一阶段

cache salt scope、warmup/provenance 门禁、seed 顺序、实际 token 合规、隐私字段和 profile 双边界均有
race/contract tests。本阶段没有访问 Kubernetes 或 GPU。R6I-6 将先检查 GPU Node Ready，再用真实 Render
收敛 byte 起点、完成 cold/warm 校准和 512–3072 token 阶梯；节点 NotReady 时立即停止。
