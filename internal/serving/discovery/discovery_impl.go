package discovery

import "fmt"

// New 根据命名模式创建 discovery 实现；具体实现仍可通过各自构造函数单独测试。
func New(config Config) (Resolver, error) {
	switch config.Mode {
	case "", ModeStatic:
		return NewStatic(config.Static)
	case ModeEndpointSlice:
		return NewEndpointSlice(config.EndpointSlice)
	default:
		return nil, fmt.Errorf("unsupported discovery mode %q", config.Mode)
	}
}
