[English](README.md) | 简体中文

# gfluxy

北向流量网关：**一个端口**承接 HTTP/1.1、HTTP/2、HTTP/3 与 WebSocket，按冻结的 `/api/*` 前缀路由，上游由注册中心（静态文件 / Nacos）提供，流量可按请求头、租户、百分比灰度分流。

> 独立中间件：不绑定任何特定业务平台，文档与示例只使用中性业务名。与 nginx / Traefik / k8s gateway / ingress 并存：边缘负责 TLS 终止，gfluxy 负责服务发现式路由与灰度。

## 状态

v0 已跑通端到端：单端口四协议、前缀剥离、Nacos 发现与健康剔除、三种负载均衡策略、三维度灰度、控制面热更新、访问日志与 Prometheus 指标。

## 快速开始

```bash
make build
./bin/gfluxy -config examples/gfluxy.yaml     # 需要先准备证书；或删掉 tls 段落走明文
```

```bash
# 业务请求（前端路径 → 后端裸路径）
curl -k https://127.0.0.1:8443/api/catalog/items

# 灰度：带头走金丝雀，响应里有 X-Gfluxy-Gray
curl -k -H 'X-Gray: 1' https://127.0.0.1:8443/api/catalog/items -i | grep X-Gfluxy-Gray

# 控制面
curl http://127.0.0.1:9000/v1/status
curl -X PUT --data-binary @routes.yaml http://127.0.0.1:9000/v1/config

# 观测
curl http://127.0.0.1:9000/metrics | grep gfluxy_
curl http://127.0.0.1:9000/readyz
```

每次请求写一行 JSON 访问日志到 stdout：`route`、`service`、`upstream`、`gray`、`status`、`bytes`、`ms`、`error`。

## 设计

- 目标与需求：[`docs/specs/goal-and-requirements.md`](docs/specs/goal-and-requirements.md)
- 架构与模块边界：[`docs/specs/architecture.md`](docs/specs/architecture.md)

关键取舍：Caddy 只承担监听、TLS、ALPN/QUIC 与 WebSocket 升级；路由、灰度、选路与反代在 gfluxy 自己的 handler 里，规则更新只换一个原子快照，**不重载数据面、不重启进程**。

## 构建与测试

```bash
make build          # bin/gfluxy
make ci             # fmt-check + vet + test + build
```

测试覆盖单端口三协议（含 h3 客户端）、前缀剥离、WebSocket 隧道、灰度命中与稳定组隔离、Nacos 元数据归一化与故障保留、控制面原子热更新。

## 技术底座

数据面以 [Caddy](https://github.com/caddyserver/caddy)（Apache-2.0）为内核：HTTP/1-2-3、WebSocket 升级、TLS 与单端口监听复用其成熟实现；gfluxy 提供四个自有部分——路由、注册中心、灰度选路、控制面。

> Caddy 是 Caddy 项目（800.com, LLC）的注册商标；本项目仅以 Apache-2.0 许可使用其代码，与该项目无隶属关系，不代表其背书。
