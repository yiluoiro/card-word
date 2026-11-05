# Card Word 宝塔部署包

适用：宝塔 Linux 面板、Nginx、systemd、x86_64 或 ARM64。程序内含前后端和 SQLite，无需安装 Go、Node.js、PHP、MySQL。部署包不包含开发数据、管理员密码、密钥或示例订单。

## 1. 上传并安装

先在宝塔「网站」中添加域名（纯静态站点即可），申请证书、开启 HTTPS 和强制 HTTPS。以下将 `shop.example.com` 作为示例，请使用你自己的域名。

把压缩包上传到 `/www/server/` 并解压，**不要放在网站公开目录中**。进入解压目录，在宝塔终端执行：

```bash
bash install.sh
```

按提示输入 `https://shop.example.com` 和监听端口，默认 `8766`。脚本识别 CPU 架构、验证 SHA256、创建独立账号并注册 systemd 开机启动。不会修改宝塔或自动覆盖已有安装。若提示路径或账号已存在，先检查现有服务；不要删除数据库或 master.key 来绕过检查。

程序：`/opt/cardword/cardword`；配置：`/etc/cardword/cardword.env`；店铺数据库、加密密钥和上传文件：`/var/lib/cardword/`。服务以 cardword 用户运行，只监听本机。

## 2. 配置宝塔反向代理

在宝塔「网站 → 设置 → 配置文件」中，将以下文件内容放进对应 HTTPS 站点的 `server { ... }` 内，替换已有的 `location /`：

```bash
cat /etc/cardword/nginx-location.conf
```

保留宝塔的证书配置和 ACME 验证规则。移除会截获 `.css`、`.js`、图片等请求的旧静态缓存 location，避免嵌入程序的资源被误当作本地文件。不要再叠加第二套宝塔代理规则。若已有 `client_max_body_size`，修改原值而不是重复添加。保存前使用宝塔的配置检查功能，或执行 `/www/server/nginx/sbin/nginx -t`，成功后重载 Nginx。

无需开放 `8766` 公网端口。默认按 Nginx 直接接受客户端连接配置可信代理；如另接 CDN，请先在 Nginx 配置可信 CDN 网段和真实 IP 解析，不要盲目信任用户提供的 X-Forwarded-For。

## 3. 网页安装

访问 `https://你的域名/install`。在宝塔终端读取安装码：

```bash
cat /var/lib/cardword/install-token.txt
```

填写安装码、站点名称、管理员用户名和密码，点击「完成安装」。完成后跳转后台登录，安装码文件被删除、安装入口关闭；不会生成演示商品。已有管理员的数据库不会再次开放安装。

未安装时 `/healthz` 返回 200 表示进程正常，`/readyz` 返回 503 表示等待安装；安装完成后 `/readyz` 返回 200。

## 4. 上线前配置

进入后台设置收款方式、人工收款说明、站点联系信息；上传商品和实际卡密后再上架。SEO 默认关闭，确认域名和内容后再在后台开启。未配置的第三方服务不会因安装而自动开通。

## 管理与备份

```bash
systemctl status cardword --no-pager
systemctl restart cardword
systemctl stop cardword
journalctl -u cardword -n 100 --no-pager
bash /opt/cardword/backup.sh
```

备份会短暂停止服务，打包完整数据、加密密钥、程序及配置，再恢复原先运行的服务。输出到 `/var/backups/cardword/`，请复制到安全的异机存储。不要只复制运行中的 `.db`，也不要丢失或重新生成 `master.key`。

## 更新已有安装

新包解压到独立目录，先执行 `sha256sum -c SHA256SUMS`。执行上述备份脚本并确认成功后，停止服务，按 CPU 架构替换 `/opt/cardword/cardword`：

```bash
systemctl stop cardword
# x86_64 示例，ARM64 请改为 bin/linux-arm64/cardword
install -m 0755 bin/linux-amd64/cardword /opt/cardword/cardword
systemctl start cardword
curl --noproxy '*' -fsS http://127.0.0.1:8766/readyz
```

如果修改了监听端口，请相应修改检查地址。不要覆盖 `/var/lib/cardword` 或 `/etc/cardword/cardword.env`。变更域名时更新 `CARDWORD_BASE_URL`、证书和 Nginx 配置后重启服务。

## 故障排查

- 502：先检查 `systemctl status cardword` 和日志，确认 Nginx 代理端口与环境配置一致。
- 安装完成后无法登录：确认通过配置的 HTTPS 域名访问；HTTPS 下 Cookie 不会发给 HTTP 地址。
- 上传提示过大：Nginx 和 `CARDWORD_MAX_BODY_BYTES` 都设为 16 MiB；应用包限 8 MiB、商品封面限 2 MiB。
- 页面样式丢失：确认 `.css/.js` 请求进入程序代理，没有被旧静态文件规则截获。
- 升级失败：保持现场，检查日志；恢复备份前停止服务，并同时恢复数据库与对应 master.key。不要通过删除数据目录尝试重装。

## 构建与验证范围

见包内 `BUILD-INFO.txt` 和 `SHA256SUMS`。本包从 Windows 交叉编译为 Linux 静态程序；网页安装、权限与回归在本机测试。Linux systemd 和宝塔 Nginx 尚需在目标服务器上按上述步骤进行实机验收。
