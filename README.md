# gfluxy

北向流量网关：**一个端口**承接 HTTP/1.1、HTTP/2、HTTP/3 与 WebSocket，按冻结的 `/api/*` 前缀路由，上游由注册中心（静态文件 / Nacos）提供，流量可按请求头、租户、百分比灰度分流。

> 独立产品，**不是** xdmp-go 的子模块；XDMP 只是它的第一个消费方。与 nginx / Traefik / k8s gateway / ingress 并存：边缘负责 TLS 终止与南北向终结点，gfluxy 负责服务发现式路由与灰度。

## 状态

`v0` 开发中（数据面与控制面尚未接入）。当前里程碑只提供可构建、可 `-version` 的骨架。

## 设计

- 目标与需求：[`docs/specs/goal-and-requirements.md`](docs/specs/goal-and-requirements.md)
- 架构与模块边界：[`docs/specs/architecture.md`](docs/specs/architecture.md)

## 构建

```bash
make build          # bin/gfluxy
./bin/gfluxy -version
make ci             # fmt-check + vet + test + build
```

## 技术底座

数据面以 [Caddy](https://github.com/caddyserver/caddy)（Apache-2.0）为内核：HTTP/1-2-3、WebSocket 升级、TLS 与单端口监听复用其成熟实现；gfluxy 提供三个自有扩展——注册中心上游、灰度选路策略、控制面 API。

> Caddy 是 Caddy 项目（800.com, LLC）的注册商标；本项目仅以 Apache-2.0 许可使用其代码，与该项目无隶属关系，不代表其背书。
