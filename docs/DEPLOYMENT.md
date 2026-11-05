# 单店铺部署

所有命令在本发行目录中执行。每个运行实例对应一个独立店铺，使用自己的数据库、密钥、支付账号和域名。

## 环境变量

| 变量 | 默认值 | 作用 |
| --- | --- | --- |
| `CARDWORD_ADDR` | `:8080` | 监听地址 |
| `CARDWORD_BASE_URL` | 从监听地址推导 | 公网访问源，例如 `https://shop.example.com` |
| `CARDWORD_DATA_DIR` | `./data` | 数据目录 |
| `CARDWORD_ENV` | `production` | 运行环境 |
| `CARDWORD_MARKET_URL` | `https://zinvmeng.com/market/index.json` | 远程应用索引 |
| `CARDWORD_MARKET_ALLOW_INSTALL` | `true` | 是否允许从商城安装 |
| `CARDWORD_PLUGIN_REQUIRE_SIGNATURE` | `false` | 强制验证插件签名 |
| `CARDWORD_PLUGIN_PUBLIC_KEYS` | 空 | 受信任的 Ed25519 公钥，逗号分隔 |
| `CARDWORD_ADMIN_USER` | `admin` | 可选的无界面初始化账号 |
| `CARDWORD_ADMIN_PASSWORD` | 空 | 设置后可初始化首位管理员，至少 12 字节 |

优先通过 `/install` 和安装码设置管理员。不要将真实密码、主密钥或数据目录提交到源码仓库。

## 反向代理

本地启动示例：

```bash
./cardword --addr 127.0.0.1:8080 --base-url https://shop.example.com --data /var/lib/cardword
```

让 HTTPS 站点把请求代理到 `http://127.0.0.1:8080`，保留 Host，并设置真实客户端协议和地址。商品图、脚本和样式同样通过程序处理。Nginx 示例见 `deploy/baota/nginx-location.conf`。

只有启用了受信任代理配置后，程序才采用代理提供的客户端地址。不要把整个源码或数据目录作为静态网站根目录。

## Docker 数据

Compose 使用 `cardword-data` 命名卷，容器由 UID 10001 运行。若改用主机目录绑定，先创建目录并给 UID 10001 写权限。

修改公网地址后重新创建容器。`/healthz` 是进程探针，`/readyz` 在安装完成前返回 503。

## 备份与升级

停止程序后备份完整数据目录，其中数据库和 `master.key` 必须配套保留。同时保留自己设置的环境变量。升级只替换程序或容器镜像，再启动服务。

应用商城公钥由应用发布者提供；项目不内置私钥或默认收款账号。配置强制签名前，先填入对应源的可信公钥。

开源版只接受单店铺部署，不提供原平台版的数据迁移或降级操作。
