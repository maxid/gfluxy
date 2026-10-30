---
id: architecture
type: architecture-design
title: gfluxy 架构与模块边界
status: draft
parent: goal
---

# gfluxy 架构与模块边界

## 总览

```
              ┌────────────── 控制面（admin 端口，HTTP/1.1） ──────────────┐
  运维/CI ───▶│ PUT /v1/config → validate → 构建不可变快照 → 原子生效        │
              └───────────────────────────┬───────────────────────────────┘
                                          │ snapshot（atomic.Value）
  客户端 ──▶ 单端口 TCP+UDP ──▶ Caddy ──▶ gfluxy handler ──▶ ReverseProxy ──▶ upstream
   (h1/h2/h3/WS)   (TLS/ALPN/QUIC)   │  路由匹配/剥前缀/灰度决策/LB 选实例
                                     │
                                     └── Registry（static / nacos）──▶ 实例列表 + LabelSet
```

三个面：

- **数据面**：Caddy 负责监听、TLS、协议协商与 ws 升级；gfluxy 的 HTTP handler 负责路由、灰度与反代。数据面配置在进程启动后**不再变化**。
- **控制面**：只读写内存快照，`PUT /v1/config` 全量校验后原子替换；不碰数据面配置。
- **观测面**：访问日志、`/metrics`、`/healthz`、`/readyz`、`/v1/status`。

## 请求生命周期

1. Caddy 接受连接，按 ALPN/QUIC 确定为 h1、h2 或 h3；WS 升级发生在 h1。
2. 交给 gfluxy handler（唯一的 catch-all handler）。
3. 从快照取路由表，按 主机 + 最长路径前缀 匹配；无匹配 → `404`。
4. 按 `StripPrefix` 改写路径。
5. 对该路由求灰度：取 hashKey → 顺序求值规则 → 得到目标 `LabelSet`（默认 `color=stable`）。
6. Registry 返回该服务当前实例（已剔除不健康），按目标 LabelSet 过滤为候选组。
7. LB 策略在候选组内选一个实例（加权轮询 / 最少连接 / 一致性哈希）。
8. `httputil.ReverseProxy` 转发，注入 `X-Forwarded-*` 与 `X-Gfluxy-Route`/`X-Gfluxy-Gray` 供排障；响应原样写回。
9. 记录访问日志与指标；实例失败计数回写 Registry。

## 包结构

| 包 | 职责 | 允许依赖 |
|---|---|---|
| `cmd/gfluxy` | 参数解析、装配、信号处理 | 以下全部 |
| `internal/config` | 静态启动配置（监听地址、TLS、admin、注册中心、日志） | 标准库 |
| `internal/control` | 控制面 API、快照校验与原子生效 | `snapshot`、`route`、`canary` |
| `internal/snapshot` | 不可变规则快照 + 版本号 | `route`、`canary` |
| `internal/route` | 路由表编译与匹配（主机/前缀/StripPrefix） | 标准库 |
| `internal/registry` | `Registry` 接口、`Endpoint`、`LabelSet`、健康计数 | 标准库 |
| `internal/registry/static` | 内联/文件实例表 | `registry` |
| `internal/registry/nacos` | Nacos 订阅适配，元数据归一化 | `registry`、nacos SDK |
| `internal/lb` | 加权轮询、最少连接、一致性哈希 | `registry` |
| `internal/canary` | 灰度规则求值、确定性哈希 | `registry` |
| `internal/server` | Caddy 装配、handler 实现、反代 | `route`、`canary`、`lb`、`registry` |
| `internal/obs` | 访问日志、metrics、健康探针 | 标准库 |

依赖方向单向：`server → canary/lb/route/registry`，`control → snapshot`，注册中心适配器只依赖 `registry` 的接口。

## 核心模型

```go
type Endpoint struct {
    Addr    string            // host:port
    Weight  int               // 默认 1
    Labels  LabelSet          // version/color/group，注册中心元数据归一化后
    Healthy bool
}

type LabelSet map[string]string // 只保留 version、color、group 三个键

type Route struct {
    Host        string   // 可空，空表示任意主机
    PathPrefix  string   // 最长前缀优先
    StripPrefix int
    Service     string   // Registry 里的服务名
    Policy      string   // wrr | least_conn | hash
    Gray        []Rule   // 顺序求值，首条命中
}

type Rule struct {
    MatchHeader  string // 头名，空表示不按头匹配
    MatchValue   string
    MatchTenant  string // 租户等值，空表示不按租户匹配
    Percent      int    // 0..100，0 表示只按显式匹配
    Target       LabelSet
}
```

## 控制面契约

| 方法 | 路径 | 语义 |
|---|---|---|
| `GET` | `/v1/config` | 返回当前生效的配置文档与版本号 |
| `PUT` | `/v1/config` | 全量校验后原子生效；校验失败返回 `422` 且旧快照不变 |
| `POST` | `/v1/validate` | 只校验，不生效 |
| `GET` | `/v1/status` | 版本号、注册中心连接状态、实例健康汇总、最近一次生效时间 |
| `GET` | `/healthz` `/readyz` | 进程存活 / 可接流量（快照已加载且至少一个上游可用） |

**为什么是全量 PUT 而不是逐资源 CRUD**：灰度规则之间有顺序语义，逐条改会产生中间态；全量文档 + 单次原子替换让「规则从哪一刻开始生效」没有歧义。逐资源 PATCH 留到有 UI 时再做。

## 热更新语义

- 快照是不可变结构，`atomic.Value` 持有；请求路径只读快照，不取锁。
- `PUT /v1/config` 顺序：解析 → 校验（路由重叠、灰度百分比、服务存在、StripPrefix 合法）→ 编译路由索引 → 写入新快照 → 版本号 +1。
- 失败即拒绝，旧快照与版本号不变；不出现「部分生效」。
- 注册中心变更不进快照，只更新各服务的实例表（同样原子替换），因此实例变动不会改变规则版本。

## 关键决策

| 决策 | 选择 | 被否方案 | 理由 |
|---|---|---|---|
| 数据面内核 | Caddy 作为库，只用其监听/TLS/h1-h2-h3/WS | 从零用 `net/http` + `quic-go` | QUIC、TLS 热加载、WS 升级的细节风险高；自研 6–10 人周 vs 复用 2–4 人周 |
| 反代位置 | gfluxy 自己的 handler 用 `httputil.ReverseProxy` | 用 Caddy `reverse_proxy` + 自定义 upstream/policy 模块 | 后者的上游列表在配置加载期固化，规则热更新要触发 Caddy 重载，与「不重载」约束冲突 |
| 配置生效 | 全量文档原子替换 | 逐资源 CRUD、Caddy admin API reload | 灰度有顺序语义，逐条改会出现中间态与部分生效 |
| Nacos 耦合度 | 只取实例与健康，元数据归一化为 `LabelSet`；发布态由控制面写入 | 直接读 `metadata.version` 做灰度 | 换注册中心或改元数据键名时，灰度规则不该跟着改 |
| 租户来源 | Host 域名推断（精确映射 → `{code}.{base-domain}` → 最左标签），可配 | 读 `X-Tenant-Id` 头、解析 token | 前两者一个可伪造、一个要鉴权；Java 侧该头本就只限根租户运营方使用 |
| 灰度哈希 | `hash(route+hashKey) mod 10000`，与实例无关 | 随机数、按实例列表取模 | 前者保证同键不反转、加实例不重排 |

## 配置示例

```yaml
listen:
  addr: ":8443"
  tls:
    cert_file: /etc/gfluxy/tls.crt
    key_file: /etc/gfluxy/tls.key
admin:
  addr: "127.0.0.1:9000"
registry:
  type: nacos
  nacos:
    servers: ["127.0.0.1:8848"]
    namespace: xdmp
    group: DEV_GROUP
tenant:
  source: host              # host | header
  base_domain: example.com
  exact:                    # 精确域名 → 租户编码
    "admin.example.com": "management"
tenant_header: X-Tenant-Id
```

动态部分（`PUT /v1/config`）：

```yaml
routes:
  - path_prefix: /api/system/
    strip_prefix: 2
    service: admin
    policy: wrr
    gray:
      - match_header: X-Gray
        match_value: "1"
        target: {version: "1.1.0"}
      - match_tenant: tenant-a
        target: {version: "1.1.0"}
      - percent: 10
        target: {version: "1.1.0"}
  - path_prefix: /api/iot/
    strip_prefix: 2
    service: iot
  - path_prefix: /api/passport/
    strip_prefix: 1
    service: iam
```

## 测试策略

- **单元**：路由最长前缀与 StripPrefix、灰度哈希的确定性与分布、加权轮询比例、实例过滤（LabelSet + 健康）、快照校验的拒绝路径。
- **集成**：`httptest` 上游 + 真实监听；h3 用 `quic-go` 客户端发请求；WS 用 echo 上游验证升级与粘性；Nacos 用假适配器注入上下线事件。
- **e2e 脚本**：`make e2e` 起 three-upstream 场景，断言 404/503/灰度比例/粘性/热更新不重启。

## 风险

| 风险 | 影响 | 缓解 |
|---|---|---|
| Caddy 的 h3 + WS + 单端口组合在真实网络下的行为 | 入口不可用 | 第 3 步先做最小监听并跑通 h3/WS 回归，再叠业务逻辑 |
| 无鉴权时租户头可伪造 | 灰度被绕过 | 文档标注为已知限制；生产由边缘剥离客户端租户头 |
| `nacos-sdk-go` 版本与 Nacos 2.x/3.x 服务端兼容 | 发现失效 | 适配层只依赖接口；先支持 Nacos 2.x gRPC，3.x 用同一 SDK 验证 |
| 全量 PUT 的并发写 | 后写覆盖先写 | 单写者语义（运维/CI），响应带版本号；后续可加 `If-Match` 乐观锁 |
