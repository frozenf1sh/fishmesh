# R6A 真实 KV 信号实验清单

这个 overlay 只用于阶段 18 的真实集群门禁。它在基础 vLLM Deployment 上开启：

- `5557/TCP`：实时 KVEvents PUB；
- `5558/TCP`：有界 replay ROUTER；
- `4096` 个 batch 的 replay buffer；
- 带 Pod IP、推理端口和模型名的稳定 topic。

topic 使用 `kv@<Pod IP>:8000@<model>`，是当前 `llm-d-kv-cache` vLLM adapter 能识别的格式。
Pod IP 来自 Downward API，不把运行时地址写死在清单里。

应用前先确认两个 vLLM 副本均为 Ready：

```bash
kubectl --kubeconfig ~/.kube/fishmesh.yaml -n kubellm get pod \
  -l app.kubernetes.io/name=qwen-vllm -o wide
kubectl --kubeconfig ~/.kube/fishmesh.yaml apply -k \
  deploy/experiments/kv-aware-signal
kubectl --kubeconfig ~/.kube/fishmesh.yaml -n kubellm rollout status \
  deployment/qwen-vllm --timeout=20m
```

实验完成后恢复基础清单，避免把仅供门禁使用的端口长期留在运行环境：

```bash
kubectl --kubeconfig ~/.kube/fishmesh.yaml apply -k deploy/inference
kubectl --kubeconfig ~/.kube/fishmesh.yaml -n kubellm rollout status \
  deployment/qwen-vllm --timeout=20m
```

这不是生产安装面。生产接入必须在 R6B 增加序列 gap、freshness、Pod UID 清理和明确降级后，
才能把 KV-aware 信号接入请求路径。
