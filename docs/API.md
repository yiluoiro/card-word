# HTTP API

所有响应为 JSON（`Content-Type: application/json`）。成功：`{"ok":true,"data":...}`；失败：`{"ok":false,"error":{"code":"...","message":"..."}}`。

## 1. 认证

机器调用使用 API 令牌（后台「令牌」页创建，只显示一次）：

```
Authorization: Bearer cwk_xxxxxxxx...
# 或
X-API-Key: cwk_xxxxxxxx...
```

令牌作用域：`products:read`、`orders:read`、`orders:write`、`cards:write`。
创建令牌时勾选「允许写入」会同时授予后两者。作用域不足返回 `403 forbidden`。

## 2. 端点

### GET /api/v1/ping
作用域：无（仅验证令牌有效）。

```json
{"ok":true,"service":"card word"}
```

### GET /api/v1/products
作用域：`products:read`。返回在售商品（最多 200 条），含实时库存。

```json
{"ok":true,"data":[{"id":1,"name":"示例会员卡","slug":"demo-card","summary":"...",
  "price_cents":990,"currency":"CNY","stock":3,"min_qty":1,"max_qty":5,"status":"active"}]}
```

### GET /api/v1/products/{slug}
作用域：`products:read`。单个商品，结构同上。

### POST /api/v1/orders
作用域：`orders:write`。`Content-Type: application/json`，未知字段会被拒绝。

```json
{ "product_id": 1, "quantity": 1, "email": "buyer@example.com",
  "coupon": "SAVE10", "driver": "manual" }
```

也可以用 `product_slug` 代替 `product_id`。

```json
{"ok":true,"data":{"order_no":"CW20261102-AB12CD","query_token":"...",
  "total_cents":891,"currency":"CNY",
  "pay_url":"https://shop.example.com/order/CW20261102-AB12CD/pay?token=...",
  "expires_at":"2026-11-02T07:15:00Z"}}
```

- 库存不足 → `409 out_of_stock`
- 商品/优惠码不存在 → `404 not_found`
- 同一请求体重复提交会命中幂等键，返回同一订单号（不返回新的 `query_token`）

### GET /api/v1/orders/{order_no}
作用域：`orders:read`。订单状态；`delivered` 时附带 `cards` 明文卡密数组。

```json
{"ok":true,"data":{"order_no":"CW20261102-AB12CD","status":"delivered",
  "quantity":1,"total_cents":990,"currency":"CNY","email":"buyer@example.com",
  "cards":["DEMO-KEY-0001-AAAA"],"paid_at":"...","delivered_at":"..."}}
```

### POST /api/v1/orders/{order_no}/close
作用域：`orders:write`。关闭待支付订单。状态不允许时返回 `409`。

### POST /api/v1/cards
作用域：`cards:write`。批量导入卡密（1–5000 条，自动去重、加密存储）。

```json
{ "product_id": 1, "secrets": ["KEY-A", "KEY-B"], "label": "batch-2026-11" }
```

```json
{"ok":true,"data":{"imported":2}}
```

## 3. 买家侧（无需令牌）

这些路由仅供浏览器使用，需要 CSRF 与查询凭证：

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/p/{slug}` | 商品页 |
| POST | `/checkout` | 下单（表单：`product_id`、`quantity`、`email`、`coupon`、`driver`、`agree`、`csrf`） |
| GET | `/order/{no}?token=...` | 订单页（凭证不符返回 403） |
| GET | `/order/{no}/status?token=...` | 状态轮询 JSON |
| GET | `/order/{no}/pay?token=...` | 发起支付 |
| GET/POST | `/query` | 订单查询表单 |

## 4. 支付回调

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| POST | `/pay/notify/{driver}` | 内置驱动回调（EPay 走 MD5 签名校验 + `trade_status`） |
| GET | `/pay/return/{driver}` | 浏览器同步回跳 |
| POST | `/api/plugins/{uid}/notify` | 插件支付回调（HMAC-SHA256，见 PLUGIN_SDK） |

回调处理原则：先验签 → 再验金额 → 最后进 `FulfillOrder`（幂等）。重复回调返回 200，不重复发卡。

## 5. 运维探针

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/healthz` | 存活探针（不查数据库） |
| GET | `/readyz` | 就绪探针（ping 数据库，失败 503） |
| GET | `/version` | 版本、commit、构建时间 |

## 6. 错误码

| code | HTTP | 含义 |
| --- | --- | --- |
| `unauthorized` | 401 | 缺少/无效令牌 |
| `forbidden` | 403 | 作用域不足、CSRF 失败、查询凭证错误 |
| `not_found` | 404 | 资源不存在 |
| `out_of_stock` | 409 | 库存不足 |
| `conflict` | 409 | 状态冲突（如关闭已支付订单） |
| `bad_request` | 400 | 参数/JSON 不合法 |
| `internal` | 500 | 服务端错误（详情只进日志，含请求 ID） |

所有响应带 `X-Request-Id`，排查时用它检索日志。
