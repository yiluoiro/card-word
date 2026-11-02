# 插件开发指南（Plugin SDK v1）

插件 = **一个 ZIP 包**，根目录必须有 `manifest.json`。安装后默认停用，管理员核对权限并启用后才会载入运行时。

## 1. 目录结构

```
my-plugin.zip
├── manifest.json
├── index.html          # static 运行时的入口
├── theme.css
└── locales/zh-CN.json
```

限制（硬编码，安装时强制）：

| 项目 | 限制 |
| --- | --- |
| ZIP 体积 | ≤ 8 MiB |
| 解压后总体积 | ≤ 32 MiB |
| 文件数 | ≤ 400 |
| 单文件 | ≤ 8 MiB |
| 路径 | 禁止绝对路径、`..`、盘符、符号链接 |

## 2. manifest.json

```json
{
  "schema": "cardword.plugin/v1",
  "uid": "com.example.wallet",
  "name": "Wallet Payment",
  "version": "1.2.0",
  "author": "Example Labs",
  "description": "支持钱包扫码支付",
  "homepage": "https://example.com/wallet",
  "kind": "payment",
  "license": "Apache-2.0",
  "runtime": {
    "type": "external",
    "base_url": "https://wallet.example.com/cardword",
    "health_path": "/healthz",
    "timeout_ms": 5000
  },
  "payment": { "title": "钱包支付", "create_path": "/payment/create" },
  "permissions": ["payment.provider", "http.outbound", "order.read"],
  "hooks": ["order.paid"],
  "settings": [
    { "key": "api_key", "label": "API Key", "type": "secret", "required": true },
    { "key": "sandbox", "label": "沙箱模式", "type": "boolean", "default": "0" }
  ]
}
```

字段规则：

- `schema`：必须是 `cardword.plugin/v1`。
- `uid`：3–64 个字符，`a-z0-9._-`，首尾为字母或数字，例如反向域名。唯一标识，重装即覆盖。
- `version`：`1.2.3` 或带 `-beta.1` 预发布后缀。
- `kind`：`payment` | `notifier` | `theme` | `extension`。
- `runtime.type`：
  - `external`：必须有 `base_url`（http/https），且**必须声明 `http.outbound`**，否则拒绝安装；
  - `static`：必须有 `static.index`；
  - `none`：仅清单型插件。
- `permissions`：白名单，超出即拒绝：
  `payment.provider`、`notifier.sender`、`theme.override`、`order.read`、`product.read`、
  `http.outbound`、`admin.page`。
- `hooks`：`order.created`、`order.paid`、`order.delivered`、`order.closed`、
  `product.updated`、`plugin.enabled`。
- `settings`：核心渲染成后台表单并加密无关地存储（`type: secret` 会掩码显示）。

JSON 解析使用 `DisallowUnknownFields`：拼错字段名会直接失败，不会静默忽略。

## 3. 签名与分发

核心用 **Ed25519** 校验整个 ZIP 字节：

```bash
# 生成密钥对（私钥务必离线保管）
go run ./cmd/cardword-sign keygen --out ./keys
# keys/ed25519.key  (base64 32 字节 seed)
# keys/ed25519.pub  (base64 公钥)

# 打包
zip -r my-plugin.zip manifest.json index.html theme.css

# 签名（输出 base64 签名到 stdout，或用 --sig 写文件）
go run ./cmd/cardword-sign sign --key ./keys/ed25519.key --in my-plugin.zip --sig my-plugin.sig
```

站长侧配置信任公钥并强制签名：

```bash
CARDWORD_PLUGIN_REQUIRE_SIGNATURE=1
CARDWORD_PLUGIN_PUBLIC_KEYS="$(cat keys/ed25519.pub)"
```

安装时提交 `package` + `signature` 两个表单字段。未配置强制签名时，允许安装未签名包（但会在后台标注"未签名"，并由管理员自行承担风险）。

## 4. 运行时契约（external）

### 4.1 核心 → 插件

所有请求：

```
POST {base_url}{path}
Content-Type: application/json
User-Agent: cardword-core
X-Cardword-Plugin: <uid>
X-Cardword-Timestamp: <unix 秒>
X-Cardword-Signature: <hex(HMAC-SHA256(secret, timestamp + "." + body))>
```

插件必须在 5 秒（`runtime.timeout_ms`）内响应 2xx，否则记为运行时错误（后台可见，不影响主流程）。

**支付创建** `POST {base_url}{payment.create_path|/payment/create}`

```json
{ "order_no": "CW20261102-AB12CD", "subject": "会员卡", "amount_cents": 990,
  "currency": "CNY", "notify_url": "https://shop.example.com/api/plugins/com.example.wallet/notify",
  "return_url": "https://shop.example.com/pay/return/plugin:com.example.wallet?order_no=...",
  "email": "buyer@example.com" }
```

**Hook 事件** `POST {base_url}/hook`

```json
{ "event": "order.paid", "occurred_at": "2026-11-02T07:00:00Z",
  "payload": { "order_no": "CW20261102-AB12CD", "total_cents": 990, "email": "buyer@example.com" } }
```

**健康检查** `GET {base_url}{runtime.health_path}`，启用插件时调用，非 2xx 则拒绝启用。

### 4.2 插件 → 核心（支付成功回调）

```
POST /api/plugins/{uid}/notify
X-Cardword-Signature: <hex(HMAC-SHA256(secret, raw_body))>

{ "order_no": "CW20261102-AB12CD", "txn_id": "wallet_8f21", "amount_cents": 990, "paid": true, "raw": "provider payload" }
```

核心校验签名 → 校验 `payment.provider` 权限 → `FulfillOrder`（金额不符会被拒绝）。成功返回 `{"ok":true}`，重复回调幂等返回 200。

插件如何拿到 secret？核心首次启用时生成并存入 `plugin_settings(uid, "_secret")`，
管理员在后台「插件配置」中可见（`type: secret` 字段不会回显）。插件侧通过部署时的
环境变量/配置注入同一个值。

## 5. 运行时契约（static）

- 文件由 `GET /assets/plugins/{uid}/{path}` 提供，路径经过清洗，仅限插件目录内。
- `static.index` 是后台「打开控制台」的跳转目标。
- 适合主题、说明页、纯前端工具；不能读取订单数据。

## 6. 本地开发与调试

```bash
# 一个最小 external 插件（任意语言皆可，这里用 Node）
cat > manifest.json <<'JSON'
{ "schema":"cardword.plugin/v1", "uid":"com.example.echo", "name":"Echo",
  "version":"0.1.0", "kind":"notifier", "runtime":{"type":"external","base_url":"http://127.0.0.1:9911","timeout_ms":3000},
  "permissions":["http.outbound"], "hooks":["order.created"] }
JSON
node -e '
  require("http").createServer((req,res)=>{let b="";req.on("data",c=>b+=c);req.on("end",()=>{
    console.log(req.method, req.url, req.headers["x-cardword-signature"], b.slice(0,120));
    res.writeHead(200,{"content-type":"application/json"});res.end("{\"ok\":true}");
  });}).listen(9911,()=>console.log("echo on 9911"));'

zip -r echo.zip manifest.json
```

上传安装 → 启用 → 下单，即可在插件进程看到带 HMAC 签名的 `order.created`。

## 7. 安全清单

- [ ] 校验 `X-Cardword-Timestamp` 并拒绝偏差超过 5 分钟的请求（防重放）
- [ ] 用**原始请求体字节**计算 HMAC，不要先反序列化再重排 JSON
- [ ] 回调必须叠加你自己的渠道签名校验，核心的 HMAC 只证明"是核心调用你的"
- [ ] 不要把 secret 写进镜像或仓库，用环境变量/密钥管理注入
- [ ] 插件进程以最小权限运行（独立用户、只读文件系统、出站白名单）
- [ ] 支付插件必须校验 `amount_cents` 与自身订单记录一致
