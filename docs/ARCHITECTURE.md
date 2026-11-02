# 架构说明

## 1. 总体结构

```
             ┌─────────────┐
  浏览器 ───▶│  反向代理    │──▶ card word (单进程)
             │ TLS / gzip  │      ├── HTTP 层（中间件 → 路由 → 处理器）
             └─────────────┘      ├── 领域层（store 事务、payment、notify）
                                  ├── 插件宿主（external HTTP / static）
                                  └── SQLite（WAL：1 写 + N 读）
```

设计目标：**单机极致简单，边界清晰可拆**。所有跨进程依赖都收敛到两个接口
（`store.DB` 与 `plugin.Host`），未来换 PostgreSQL 或拆服务不需要改处理器。

## 2. 存储与并发

- SQLite 打开两个连接池：写池 `MaxOpenConns=1` 且 `_txlock=immediate`，读池为 `2×CPU`。
  WAL 模式下读不阻塞写，写串行化消除了 `database is locked` 抖动。
- 所有写操作通过 `DB.tx` 包一层 IMMEDIATE 事务；`busy_timeout` 默认 10s，用
  `CARDWORD_DB_BUSY_TIMEOUT` 调整。
- 时间统一存 Unix 秒（UTC），展示时转本地时区，避免部署时区差异导致的对账问题。
- 迁移：`schema.sql` 全部为 `IF NOT EXISTS`，配合 `PRAGMA user_version` 演进。
- 备份：`PRAGMA wal_checkpoint(TRUNCATE)` 在优雅停机时执行；运维侧直接冷拷贝
  `cardword.db` + `master.key` 即可（两者必须成对，否则卡密不可解密）。

## 3. 钱的唯一边界

`store.FulfillOrder(orderNo, driver, txnID, amountCents, raw)` 是唯一能把订单变成
「已支付/已发货」的函数，事务内依次完成：

1. 载入订单，`delivered` 直接返回 `AlreadyDone`（幂等）；
2. `closed` 返回 `ErrInvalidStatus`（需人工介入，不吞掉支付记录）；
3. 金额不符 → 拒绝（防止回调伪造或改价）；
4. 写 `payments` 行，`UNIQUE(driver, txn_id)` 冲突即视为重复回调；
5. `SELECT ... LIMIT quantity` 取可用卡 → `UPDATE ... WHERE id IN (...) AND status='available'`
   → 受影响行数不等于数量则整体回滚（不超卖）；
6. 写 `order_cards`、更新订单状态、累加优惠码使用次数。

库存不足但钱已到账时，订单进入 `paid`（待补发），后台可「补发卡密」，避免吞单。

## 4. 认证与会话

- 密码：Argon2id，`m=64MiB, t=3, p=2`，盐 16B，输出 32B，`$argon2id$...` 自描述格式。
- 会话：随机 32B 令牌，数据库只存 SHA-256 摘要；`SameSite=Lax`、`HttpOnly`、HTTPS 下 `Secure`。
- 双超时：绝对 `SessionTTL`（默认 12h）+ 空闲 `SessionIdle`（默认 2h），每次请求滑窗刷新。
- 登录成功即删除该账号的其它会话（会话轮换），降低 cookie 被窃后的存活窗口。
- 登录失败与不存在用户走同样的 Argon2id 计算量，避免通过响应时间枚举账号。

## 5. CSRF 与浏览器安全

- 双提交令牌：`cw_csrf` cookie（可读）+ 表单 `csrf` 字段/`X-CSRF-Token` 头，比较用常量时间。
- 同源校验：优先 `Sec-Fetch-Site: same-origin|none`；否则解析 `Origin`，与请求 `Host`
  或配置的 `base_url` 比较；`Origin: null`（沙箱 iframe/本地预览）放行但**仍需 CSRF 令牌**。
- 响应头：严格 CSP（`script-src 'self'`，无内联脚本）、`frame-ancestors 'none'`、
  `nosniff`、`Referrer-Policy: no-referrer`、COOP/CORP、HTTPS 下 HSTS。
- 前端 JS 全部走 `data-*` 属性驱动（自动提交支付表单、复制卡密、轮询订单状态），不注入内联脚本。

## 6. 插件运行时

| 运行时 | 用途 | 隔离方式 |
| --- | --- | --- |
| `static` | 主题、纯前端页面 | 只读文件服务，`/assets/plugins/<uid>/` |
| `external` | 支付、通知、复杂集成 | 独立进程/服务，HTTPS + HMAC-SHA256 双向签名 |
| `none` | 文档型、纯配置插件 | 仅 manifest |

核心 → 插件的调用带三个头：`X-Cardword-Plugin`、`X-Cardword-Timestamp`、
`X-Cardword-Signature = HMAC(secret, timestamp + "." + body)`；插件 → 核心的回调
`POST /api/plugins/{uid}/notify` 用同一 secret 校验。secret 由核心生成并存于
`plugin_settings`，不出现在插件包里。

插件永远拿不到：
- 管理员会话 cookie（不转发、不注入）；
- `master.key`（卡密解密只在核心内）；
- 数据库连接（只能通过被授权的 API/Hook）。

## 7. 高可用实践

单进程内已具备：优雅停机（20s 排空）、探针、连接池上限、超时与限流、崩溃自动重启（systemd/Docker restart）。

水平扩展有两个成熟路径：

1. **读多写少**：反代把 `/static/*`、`/assets/plugins/*` 直出，核心只处理动态请求；
   多实例共享同一 SQLite 文件（NFS/块存储）仅建议用于**只读副本**。
2. **多写实例**：把 `store` 换成 PostgreSQL 实现（接口已隔离：所有 SQL 与事务都封在
   `internal/store`），会话表与幂等键天然支持多实例；插件宿主是无状态的。

不要用 SQLite 承载多写实例——WAL 是单机语义，这是本项目明确的边界，而不是隐藏的坑。

## 8. 目录与扩展点

| 想改什么 | 改哪里 |
| --- | --- |
| 新增支付渠道 | 实现 `payment.Driver` 并 `Register`，或写成 `external` 插件 |
| 新增通知渠道 | 实现 `notify.Sender`，或订阅 Hook 的插件 |
| 换存储 | 实现与 `store.DB` 相同的方法集 |
| 换主题/前端 | `internal/httpapi/web/*`（内嵌，改完重新编译即可） |
| 新增后台页面 | `httpapi.routes()` + 模板 + 权限包装 `s.admin(...)` |
