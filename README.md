# card word

轻量、高性能、可插件化的自动发卡系统。单二进制 + SQLite，内置**应用商城**（插件上传 / 下载 / 签名校验 / 启停管理），面向虚拟商品、卡密、激活码等自动发货场景。

Lightweight, high-performance, plugin-extensible digital goods delivery platform ("card selling"). One binary, SQLite storage, and a built-in plugin marketplace.

[![CI](https://github.com/yiluoiro/card-word/actions/workflows/ci.yml/badge.svg)](https://github.com/yiluoiro/card-word/actions/workflows/ci.yml)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

---

## 特性

**性能与部署**
- Go 单二进制 + 内嵌前端资源，无 Node/PHP 运行时依赖，启动毫秒级
- SQLite WAL：1 个写连接 + N 个读连接池，读写分离、事务内发卡，实测并发下单不超卖
- 优雅停机、`/healthz` 与 `/readyz` 探针、结构化 JSON 日志、请求 ID 贯穿全链路
- 支持 Docker、systemd、Nginx/Caddy 反向代理（见 `docs/ARCHITECTURE.md`）

**发卡核心**
- 商品 / 分类 / 卡密库存 / 订单 / 优惠码（百分比、固定金额、限次、限商品、有效期）
- 订单号查询凭证 + 邮箱自动发货通知（SMTP）
- 幂等下单（防重复提交）、超时自动关单、支付回调幂等与金额校验
- 卡密 AES-256-GCM 加密落库，主密钥与数据库分离，数据库泄露不等于卡密泄露

**应用商城与插件系统**
- 插件 = 签名 ZIP 包（`manifest.json` + 资源），上传后解析校验、默认停用、启用前审阅权限
- Ed25519 签名校验（可配置为强制）；SHA-256 摘要固定；防 Zip-Slip、符号链接、体积炸弹
- 权限白名单：`payment.provider`、`notifier.sender`、`theme.override`、`order.read`、`http.outbound` 等
- 运行时隔离：`external`（HTTP 服务，HMAC-SHA256 双向签名）与 `static`（纯前端主题）两类
- 生命周期事件总线：`order.created`、`order.paid`、`order.delivered`、`order.closed`、`product.updated`
- 内置商城目录 + 可选远程索引（HTTPS + 摘要校验），也支持离线上传安装

**安全**
- 管理员密码 Argon2id（64 MiB / t=3 / p=2），登录恒定时间校验、失败即审计
- 服务端会话（仅存 SHA-256 摘要）+ 空闲/绝对双超时 + 登录即轮换全部旧会话
- CSRF：双提交令牌 + 同源校验 + `SameSite=Lax`；严格 CSP、HSTS、`nosniff`、`frame-ancestors none`
- 令牌桶限流（全局 + 登录专项）、请求体上限、参数化 SQL、`DisallowUnknownFields` 严格 JSON
- 全部敏感操作写入不可变审计日志（操作者、IP、目标、详情）

---

## 快速开始

```bash
# 1. 构建
go build -trimpath -ldflags "-s -w" -o cardword ./cmd/cardword

# 2. 启动（首次启动自动建库、建管理员、写入演示商品）
CARDWORD_ADMIN_PASSWORD='ChangeMe-please-123' \
CARDWORD_BASE_URL='http://127.0.0.1:8080' \
./cardword --addr :8080 --data ./data

# 3. 打开
# 前台  http://127.0.0.1:8080
# 后台  http://127.0.0.1:8080/admin   (用户名 admin)
```

未设置 `CARDWORD_ADMIN_PASSWORD` 时，首次启动会生成随机密码并写入 `data/initial-admin.txt`（权限 0600），登录后请立即修改并删除该文件。

### 常用环境变量

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `CARDWORD_ADDR` | `:8080` | 监听地址 |
| `CARDWORD_BASE_URL` | 由 addr 推导 | **必须**填公网地址，用于支付回调与同源校验 |
| `CARDWORD_DATA_DIR` | `./data` | 数据目录 |
| `CARDWORD_MASTER_KEY` | 自动生成 `data/master.key` | 32 字节 base64/hex 主密钥，卡密加密用 |
| `CARDWORD_SESSION_TTL` / `CARDWORD_SESSION_IDLE` | `12h` / `2h` | 会话绝对/空闲超时 |
| `CARDWORD_ORDER_TTL` | `15m` | 待支付订单保留时长 |
| `CARDWORD_RATE_LIMIT_RPS` / `_BURST` | `20` / `60` | 全局限流 |
| `CARDWORD_PLUGIN_REQUIRE_SIGNATURE` | `false` | 强制插件签名 |
| `CARDWORD_PLUGIN_PUBLIC_KEYS` | 空 | 信任的 Ed25519 公钥（base64，逗号分隔） |
| `CARDWORD_MARKET_URL` | 空 | 远程商城索引地址（`https://`） |
| `CARDWORD_TRUSTED_PROXIES` | 空 | 信任的反代 IP，开启后读取 `X-Forwarded-For` |

支付与 SMTP 在后台「系统设置」中配置（EPay 网关/商户号/密钥、SMTP 主机/账号/发件人）。

---

## 架构

```
cmd/cardword            # 入口：配置、启动、优雅停机、定时维护
internal/
  config                # 环境变量/flag 解析与校验
  cryptoutil            # Argon2id、AES-GCM、HMAC、随机令牌、掩码
  store                 # SQLite(WAL) 仓储：用户/会话/商品/卡密/订单/优惠码/插件/审计
  payment               # 驱动注册表 + EPay + 人工确认
  notify                # SMTP 发送与异步派发
  plugin                # manifest 校验、签名安装器、运行时宿主、Hook 总线
  market                # 商城索引与包下载（摘要校验）
  httpapi               # 路由、中间件、店面、后台、REST API、插件回调
    web/templates       # 内嵌 HTML 模板
    web/static          # 内嵌 CSS/JS（无外链 CDN）
```

关键不变量：

1. **钱只在一个地方变成货**：`store.FulfillOrder` 在单个 IMMEDIATE 事务里完成「校验金额 → 幂等 → 扣卡 → 关联订单 → 记支付」，任何渠道回调都只走这里。
2. **卡密只以密文存在**：`card_keys.secret` 是 AES-256-GCM 密文，AAD 绑定商品 ID，跨记录搬运会解密失败。
3. **插件默认无权**：安装后 `enabled=0`，启用时才载入运行时；未声明 `http.outbound` 的外部运行时直接拒绝安装。

更多见 [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md)、[`docs/PLUGIN_SDK.md`](docs/PLUGIN_SDK.md)、[`docs/API.md`](docs/API.md)。

---

## 插件：一个最小的主题包

```json
// manifest.json
{
  "schema": "cardword.plugin/v1",
  "uid": "com.example.hello",
  "name": "Hello Theme",
  "version": "1.0.0",
  "author": "you",
  "kind": "theme",
  "runtime": { "type": "static" },
  "static": { "index": "index.html" },
  "permissions": ["theme.override"],
  "hooks": []
}
```

打包并签名：

```bash
# 生成签名密钥（私钥自己保管，公钥配置到 CARDWORD_PLUGIN_PUBLIC_KEYS）
go run ./cmd/cardword-sign keygen --out ./keys

# 打包 + 签名
zip -r hello-theme.zip manifest.json index.html theme.css
go run ./cmd/cardword-sign sign --key ./keys/ed25519.key --in hello-theme.zip
```

在后台「应用商城 → 上传安装」提交 ZIP 与签名，核对权限后启用。

---

## 测试

```bash
go test ./... -count=1
```

覆盖：订单生命周期、**12 并发抢 5 张卡不超卖**、幂等回调、金额不符拒绝、优惠码计次、密文落库与错密钥解密失败、插件路径穿越/权限/签名策略、CSRF 拒绝、API 作用域，以及 16 个页面的渲染冒烟测试。

---

## 部署

```bash
docker compose up -d --build
```

数据落在 `./data`（SQLite + 插件目录 + 主密钥）。生产建议：反向代理终止 TLS、`CARDWORD_TRUSTED_PROXIES` 仅填反代地址、定期备份 `cardword.db*` 与 `master.key`（两者需成对备份，否则卡密无法解密）。

---

## 开源协议

[Apache License 2.0](LICENSE)。选择它而不是 MIT 的原因：显式专利授权与贡献条款，更适合「可商用 + 接受外部插件贡献」的基础设施软件。

## 路线图

- [ ] 多管理员账号与 RBAC 角色
- [ ] 购物车/多商品订单
- [ ] 插件沙箱升级为 WASM（当前为进程外 HTTP + HMAC）
- [ ] PostgreSQL 存储后端（Store 接口已隔离）
