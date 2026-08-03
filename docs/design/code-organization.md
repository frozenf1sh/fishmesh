# FishMesh Go 代码组织规范

> 状态：强制执行，2026-08-09。新代码立即遵守；旧代码按
> [`serving-domain-redesign.md`](serving-domain-redesign.md) 分阶段迁移，不为了“看起来统一”一次性搬完。

这份规范解决三个问题：第一次打开一个包时知道它提供什么；只看 import 就能判断依赖方向；
打开一个文件时能按固定顺序找到类型、入口和辅助函数。

本文中的关键词含义如下：

- **必须**：违反时不能合并；
- **应该**：默认遵守，例外必须在 review 或阶段文档中说明原因；
- **禁止**：不得通过换名绕过；
- **可以**：根据包的实际复杂度选择。

## 1. 采用什么架构

FishMesh 采用：

> **限界上下文 + 能力包 + 单向依赖 + 显式组合根**

这里的“domain”不是要求引入 Repository、Aggregate 或 Event Bus。一个 domain 就是一个具有
明确输入、输出和状态所有权的能力包，例如 discovery、routing、circuit 或 transport。

严格规范适合 FishMesh，因为请求路径包含多个天然能力边界，并且需要同时复用于 standalone
Gateway 和未来 EPP/llm-d adapter。但必须做 Go 化调整：

1. 接口用于替换边界，不用于装饰每个 struct；
2. 包名表达能力，不用 `common`、`shared`、`manager` 隐藏所有权；
3. 组合根负责创建实现，domain 构造函数不偷偷读取环境或创建外部依赖；
4. `_impl.go` 是阅读约定，不是编译器隔离边界；真正边界仍由 package 和 import DAG 保证。

## 2. 包划分规则

### 2.1 一个包必须能用一句话说明

包说明必须采用下面的形式：

> `package X` owns `<一种状态或能力>` and provides `<一个主要结果>`.

如果一句话中出现两个互不依赖的“以及”，说明应该拆包。如果两个包总是一起修改、不能独立
测试且没有不同的调用者，说明不应该拆包。

### 2.2 包必须拥有自己的数据

- 类型放在决定其不变量和生命周期的包中，而不是放在“引用次数最多”的包中；
- 产生数据的 adapter 不自动拥有数据契约，使用并解释该数据的 domain 才拥有；
- 跨包只传稳定值对象、只读 snapshot 或小接口；禁止暴露内部 map、mutex、channel；
- `routing.Backend` 这种“因为历史原因大家都 import”的类型中心必须拆除；
- 禁止新增 `shared`、`common`、`utils`、`helpers`、`base`、`misc` 包；
- 真正跨上下文且语义稳定的协议类型，必须先写 ADR 说明所有权，再建立专门命名的包。

### 2.3 依赖必须单向

- 原子 domain 不得 import 编排层或 delivery 层；
- 纯策略不得 import HTTP、Kubernetes、Prometheus 或进程配置；
- 外部 I/O 只能出现在明确的 adapter 实现文件中；
- `cmd/*` 是最终组合根，可以依赖各能力包；任何 internal 包都不得反向 import `cmd`；
- 同一层出现双向数据需要时，先重新判断类型所有权，不得用 callback 或空接口伪造环依赖；
- import cycle 永远是设计错误，不允许通过复制类型解决。

每次新建包或改变跨包 import，review 必须回答：

1. 这个包唯一拥有的能力或状态是什么？
2. 谁调用它？
3. 它依赖谁？
4. 删除它时会影响哪些包？
5. 为什么不能放进已有包？

### 2.4 包大小不是拆分理由

行数只能触发 review，不能单独证明需要拆包。拆包依据必须是能力、状态生命周期或依赖方向。

- 生产文件应该少于 300 行；超过 400 行必须拆分或记录例外；
- 函数应该少于 40 行；超过 60 行必须拆分或记录例外；
- protocol DTO、生成代码和大表驱动数据可以例外，但不得与业务流程混写。

## 3. 每个包的文件角色

新建能力包必须先创建与包同名的契约文件。以 `discovery` 为例：

```text
discovery/
├── discovery.go                    # 包说明、公开契约、公开类型和错误
├── discovery_impl.go               # 只有一个默认实现时使用
├── static_impl.go                  # 多实现时按实现命名
├── endpointslice_impl.go
├── discovery_test.go               # 契约与公开行为测试
├── static_impl_test.go
└── endpointslice_impl_test.go
```

### 3.1 强制命名

- `<package>.go`：必须存在，是阅读入口；禁止放具体外部协议解析；
- `<package>_impl.go`：只有一个默认实现时使用；
- `<variant>_impl.go`：有多个实现时按能力或协议命名；
- `<package>_test.go`：验证契约、不变量和所有实现应共有的行为；
- `<variant>_impl_test.go`：验证某个实现的边界与故障；
- `<protocol>_types_impl.go`：只有大量私有 wire DTO 影响主实现阅读时使用；
- `doc.go`：新 Serving 包默认禁止；包说明写在 `<package>.go`。超长文档确有需要时才例外；
- `helpers.go`、`utils.go`、`misc.go`、`types.go`：禁止。文件名必须说明业务角色。

测试文件不要求与生产文件一一对应。一个契约测试可以验证多个实现；不要为了目录对称制造
空测试。

### 3.2 接口规则

出现以下任一情况时必须定义接口：

- 编排层需要注入两个及以上实现；
- 边界连接 Kubernetes、Prometheus、HTTP upstream、时钟或文件系统，测试需要替身；
- standalone 与 integrated runtime 需要共享同一能力契约；
- 调用方只应看到实现能力的一小部分。

以下情况禁止创建接口：

- 只有一个实现、没有替换需求，只是为了得到 `Xxx interface`；
- 接口与实现一一对应且暴露实现的全部方法；
- 接口只用于 mock，而一个小函数参数或具体类型已经足够；
- 使用 `any`、空接口或超大 `Manager` 接口逃避领域建模。

接口通常放在能力提供包的 `<package>.go`，便于按统一入口阅读。若某个调用方只需要更窄的
能力，可以在调用方定义局部接口，这是允许且更符合 Go 的做法。接口应该只有 1–5 个方法；
超过 5 个必须拆分职责或说明原因。

具体实现必须包含编译期检查：

```go
var _ Resolver = (*endpointSliceResolver)(nil)
```

实现类型默认不导出，例如 `endpointSliceResolver`。只有调用方确实需要该具体类型独有的能力时，
才允许导出实现类型；不能仅为了让构造函数返回 concrete type 而导出。

构造函数只做依赖检查、配置校验和内部状态初始化，不读取环境变量，不启动无法关闭的 goroutine，
不使用隐藏的全局 client。返回接口还是具体类型由调用者需要决定，不为了隐藏名字而强制统一。

## 4. 单文件内的固定顺序

所有非生成 `.go` 文件必须按以下顺序组织：

1. package comment（仅同包名入口文件）；
2. `package`；
3. `import`：标准库、空行、项目内包、空行、第三方包；由 `gofmt` 排序；
4. `const`：先导出协议常量，再私有默认值；按同一概念分组；
5. `var`：sentinel error、编译期接口检查；禁止可变全局状态；
6. 导出类型：值对象、配置、接口；
7. 私有实现类型及其状态；mutex 必须紧邻它保护的字段；
8. 构造函数：`New...`、`Default...`、解析入口；
9. 导出函数与导出方法：按接口方法顺序；
10. 私有 receiver 方法：按主流程调用顺序；
11. 纯辅助函数；
12. 私有 wire DTO 只允许放在文件末尾，过多时移到 `<protocol>_types_impl.go`。

禁止在辅助函数之后突然声明业务常量或主要 struct。禁止用多处小 `const`/`type` 块打断执行
流程。测试文件同样先声明 fixture 类型，再写测试，再写测试辅助函数。

## 5. 编排函数规范

编排层负责“先做什么、后做什么”，原子 domain 负责“具体怎么做”。编排函数必须让初学者
只看主流程就能理解一次请求。

```go
func (s *service) Select(ctx context.Context, request Request) (Lease, error) {
	// 1. 读取当前可用后端和观测快照。
	snapshot, err := s.snapshot.Build(ctx)
	if err != nil {
		return Lease{}, err
	}

	// 2. 根据故障状态和调度策略选择后端。
	decision, err := s.router.Select(request.RoutingKey, snapshot)
	if err != nil {
		return Lease{}, err
	}

	// 3. 为已选后端登记请求生命周期。
	return s.leases.Acquire(decision), nil
}
```

强制要求：

- 依赖通过 struct 字段注入，禁止在业务方法里 `New...` 外部能力；
- 一个编排方法只表达 3–7 个阶段；阶段之间保留空行；
- 3 个以上阶段使用 `// 1.`、`// 2.` 注释，注释说明目的，不复述函数名；
- 不在编排层解析 Prometheus、EndpointSlice JSON 或实现 hash/EWMA；
- 不在原子实现中偷偷 fallback 到另一个 domain；fallback 由编排层明确决定并记录 reason；
- 错误在当前层增加语义后 `%w` 返回，禁止记录后又无条件返回同一错误造成重复日志；
- 资源获取必须在同一视觉块中安排 release，或返回具有幂等 `Release/Complete` 的 lease；
- 请求路径禁止 fire-and-forget goroutine。

## 6. 常量、字面量和错误规范

“消灭所有字面量”同样会降低可读性。判断标准不是出现次数，而是它是否表达稳定语义。

### 6.1 必须命名的值

- routing mode、policy、reason、status、outcome；
- HTTP header 名、环境变量名、metric/label 名；
- Kubernetes resource、annotation、label key；
- timeout、容量、阈值、buffer size、响应体上限；
- 在两个以上位置必须保持相同的协议字符串；
- `499` 这类非标准或不直观状态码。

字符串枚举必须使用自定义类型：

```go
type Reason string

const (
	ReasonAffinityHit Reason = "affinity-hit"
	ReasonCircuitOpen Reason = "circuit-open"
)
```

常量由解释其语义的 domain 拥有。Gateway 只消费 `routing.Reason`，不能重新声明同值字符串。

### 6.2 可以保留的值

- `len(items) == 0`、循环步长 `1`、切片起点等局部算法值；
- 标准库已经命名的 `http.StatusBadGateway`、`io.EOF`；
- 只出现一次、用于结构化日志检索的完整消息；
- 测试用例中清晰表达输入的值。

禁止 `const zero = 0`、`const one = 1` 这类只把语法变长的命名。默认值集中在所属 Config
附近，环境变量名不得散落在解析函数中。

### 6.3 错误

- 可供调用方判断的错误使用 package-owned sentinel 或 typed error；
- 仅供人阅读的错误直接 `fmt.Errorf`，包含动作和关键标识；
- 跨层包装使用 `%w`；
- 不通过比较错误字符串控制流程；
- client cancellation、deadline、transport failure、upstream HTTP status 和 downstream write
  failure 必须保持不同 outcome，不得收敛成一个 `failed bool` 穿过所有层。

## 7. 类型、配置和并发规范

- ID、Mode、Reason、Status、Outcome 等业务标识使用命名类型，不使用任意 string；
- 配置按 domain 拆分；一个 30 字段的 Gateway Config 不能直接传给每个子包；
- 环境变量只在 composition/config 边界解析一次，domain 只接收已验证配置；
- snapshot 在发布后视为不可变；返回 map/slice 时复制或明确所有权转移；
- 每个 goroutine 必须有 owner、cancel 路径和 wait 路径；
- channel 必须说明容量、关闭方和背压语义；
- mutex 与被保护字段放在同一 struct 的相邻位置，并注明保护范围；
- 资源状态必须有容量上限、TTL、membership reconcile 或 `Close` 中的明确回收。

## 8. 测试与架构门禁

每个 domain 至少覆盖：

1. 契约正常路径；
2. 无数据、错误、取消或过期边界；
3. 有状态实现的回收与并发；
4. 所有实现共享的 contract test（存在多实现时）。

重构提交必须保持行为不变，并通过：

```bash
gofmt -w <changed-go-files>
go test -race ./...
go vet ./...
go build ./...
make manifest
git diff --check
```

目标目录落地后增加自动架构测试，基于 `go list -json` 拒绝以下依赖：原子 domain →
`gateway/requestpath/cmd`、routing → infrastructure、workload → serving internals。自动门禁完成前，
每个重构提交必须在阶段文档附上 `go list` 依赖审查结果。

## 9. Review 清单

- [ ] 包能用一句话说明，且没有第二种无关状态所有权；
- [ ] 同包名契约文件是清晰阅读入口；
- [ ] 接口有真实替换边界，方法不超过合理范围；
- [ ] import 符合目标 DAG，没有 `shared/common/utils`；
- [ ] 文件声明和函数顺序符合第 4 节；
- [ ] 协议字面量与非直观限制已由所属 domain 命名；
- [ ] 编排函数只包含步骤与错误/fallback 分支；
- [ ] goroutine、channel、mutex、lease 和 endpoint state 有 owner 与回收路径；
- [ ] 测试覆盖取消、错误、过期和回收，不只覆盖 happy path；
- [ ] 阶段文档说明本次迁移了什么、没有改变什么。
