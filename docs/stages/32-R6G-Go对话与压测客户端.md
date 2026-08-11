# 阶段 32：R6G Go 对话与压测客户端

## 1. 契约与不变量

R6G 增加 `fishmesh-client`，供人类对话、单次请求验证和有限负载 profile 使用。它是 Gateway 的
外部 OpenAI/SSE 客户端，所有实现位于 `internal/workload/client` 与 `cmd/fishmesh-client`；不会扩张已
冻结的 `fishmesh-loadgen`，也不会进入 Gateway 镜像、默认 Kustomize 或 Serving dependency DAG。

1. `chat` 的本地 history 是普通 OpenAI messages JSON；未指定路径时使用用户私有状态目录下的 UTC 纳秒时间戳
   文件，显式路径仍可用于恢复同一段对话；不含 API key、
   响应头、routing key、token IDs 或 benchmark 输出。写入采用同目录临时文件加 rename，不能半写 history。
2. 每次请求均保留 HTTP status、TTFT、总时长和固定 allowlist 的 `X-FishMesh-*` 决策头。未知响应头、
   prompt、API key 与 SSE 原始 payload 不进入结构化 benchmark record。
3. SSE 只在收到首个非 `[DONE]` `data:` event 时计 TTFT；客户端必须 drain 至 `[DONE]`，否则 request
   是 canceled/failed，不得写为成功样本。
4. `bench` 的 `uniform`、`shared-prefix`、`hot-prefix` 与 `conversation` 模式确定地产生请求；原始
   JSONL 为 append-only 的 metadata → request → summary，失败 attempt 必须保留。
5. 默认 profile 固定为并发 4、max tokens 32、请求数 200、90 秒超时；CLI 对超过默认并发的负载要求
   显式 `--allow-high-concurrency`。它不会 rollout、clear cache、切换路由模式或自行启动第二个 workload。
6. `available + cached-prefix=0` 是真实 miss；`match-unavailable`/其他 unavailable 状态不参与
   cached-prefix hit/miss 汇总。R6F 的 histogram 与 client JSONL 对此语义一致。
7. 面向人的 request/chat 诊断在终端默认可用 ANSI 色彩突出 TTFT、policy、route reason、KV-aware status 与
   backend；非终端输出保持纯文本，并可由 `--color auto|always|never` 显式控制。JSONL 恒为无颜色机器格式。

## 2. CLI 面

```text
fishmesh-client chat    [--history ~/.local/state/fishmesh/chat.json]
fishmesh-client request --prompt '…' [--system '…']
fishmesh-client bench   --mode uniform|shared-prefix|hot-prefix|conversation --output run.jsonl
```

- `chat`：交互式多轮 history、SSE 文本输出和每轮固定路由头；
- `request`：一次可脚本化流式请求，输出文本、headers、TTFT/总时长；
- `bench`：可复现 profile，多模式、固定默认资源纪律和 JSONL request evidence。

所有 API key 仅从 `FISHMESH_API_KEY` 在 cmd 组合根读取并放入 HTTP `Authorization` header；history/JSONL
不会写入该值。默认 endpoint 是本机 port-forward 的 `http://127.0.0.1:8080`。

## 3. 验收

同包 contract tests 覆盖：流式 header/TTFT、非 2xx、未完成 SSE、history 原子 round trip、available zero
miss 与 unavailable 的区分、所有 benchmark mode、失败 request 保留及并发纪律。真实集群只执行一轮短
`request` 和少量 `bench` 请求；不运行长于 30 分钟或并行 GPU workload，最后确认 session-key 未变。

本地同包测试和 `go test ./cmd/fishmesh-client` 通过。2026-08-13 参考集群先确认 Gateway 1/1、vLLM 2/2
Ready 且 `FISHMESH_ROUTING_MODE=session-key`，然后只执行了一个 `request`、一个单并发两请求
`shared-prefix` profile 和一行 `chat`：三者均返回 HTTP 200、完整 SSE，且分别打印/记录
`session-key-v1`、`missing-session-key-load-balanced`、`kv_status=not-requested`。profile 的两个 JSONL
request records 均为成功、cached-prefix sample 为 false，表明没有把非 KV-aware 的状态误记为零命中；chat history
写入 system/user/assistant 三条普通 messages。没有切换路由模式、cache 或 Pod；验收后再次读取仍为
`session-key`。本次短验收无需 GPU watchdog 介入。

完成后可用 R6G 的 JSONL 与 R6F metrics 做一次独立的有限复测；R6E/llm-d 仍不在本阶段范围内。
