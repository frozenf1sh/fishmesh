// Package main 提供阶段 R6A 的真实 KV 信号探针。
//
// 探针只验证 vLLM Render、KVEvents、replay 与上游索引能否形成闭环，不进入 FishMesh
// 产品请求路径。数据源门禁通过后，生产能力会按 tokenization、kvcache、routing 的边界重新实现。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

const (
	defaultListenAddress = "127.0.0.1:19090"
	defaultFreshnessTTL  = 5 * time.Second
	defaultReplayPeriod  = 2 * time.Second
	shutdownTimeout      = 5 * time.Second
)

var errNoBackend = errors.New("至少需要一个 --backend")

// backendConfig 描述一个真实 vLLM Pod 的三条边界。
// ID 必须与 KVEvents topic 中的 Pod 标识一致，否则上游索引无法把事件归属和候选后端对应起来。
type backendConfig struct {
	ID             string
	HTTPURL        string
	EventsEndpoint string
	ReplayEndpoint string
}

// backendFlags 允许命令行重复传入 --backend。
type backendFlags []backendConfig

// String 返回适合 flag 帮助信息的简短表示。
func (f *backendFlags) String() string {
	items := make([]string, 0, len(*f))
	for _, backend := range *f {
		items = append(items, backend.ID)
	}
	return strings.Join(items, ",")
}

// Set 解析 ID,HTTP_URL,EVENTS_ENDPOINT,REPLAY_ENDPOINT。
func (f *backendFlags) Set(value string) error {
	parts := strings.Split(value, ",")
	if len(parts) != 4 {
		return fmt.Errorf("backend 必须是 ID,HTTP_URL,EVENTS_ENDPOINT,REPLAY_ENDPOINT")
	}

	backend := backendConfig{
		ID:             strings.TrimSpace(parts[0]),
		HTTPURL:        strings.TrimRight(strings.TrimSpace(parts[1]), "/"),
		EventsEndpoint: strings.TrimSpace(parts[2]),
		ReplayEndpoint: strings.TrimSpace(parts[3]),
	}
	if backend.ID == "" || backend.HTTPURL == "" || backend.EventsEndpoint == "" || backend.ReplayEndpoint == "" {
		return fmt.Errorf("backend 的四个字段均不能为空")
	}
	*f = append(*f, backend)
	return nil
}

func main() {
	log.SetLogger(zap.New(zap.UseDevMode(true)))

	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "R6A 探针失败: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var backends backendFlags
	listenAddress := flag.String("listen", defaultListenAddress, "只读探针 HTTP 监听地址")
	freshnessTTL := flag.Duration("freshness-ttl", defaultFreshnessTTL, "replay 心跳超过该时长后信号失效")
	replayPeriod := flag.Duration("replay-period", defaultReplayPeriod, "replay 心跳与补偿周期")
	flag.Var(&backends, "backend", "重复传入 ID,HTTP_URL,tcp://IP:5557,tcp://IP:5558")
	flag.Parse()

	if len(backends) == 0 {
		return errNoBackend
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx = log.IntoContext(ctx, log.Log)

	service, err := newProbe(ctx, backends, *freshnessTTL, *replayPeriod)
	if err != nil {
		return err
	}
	defer service.Close()

	server := &http.Server{
		Addr:              *listenAddress,
		Handler:           service.Handler(),
		ReadHeaderTimeout: 3 * time.Second,
	}

	serverErrors := make(chan error, 1)
	go func() {
		log.FromContext(ctx).Info("R6A 探针已启动", "listen", *listenAddress, "backends", len(backends))
		serverErrors <- server.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("探针 HTTP 服务退出: %w", err)
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("关闭探针 HTTP 服务: %w", err)
	}
	return nil
}
