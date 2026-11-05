<p align="center">
  <img src="./internal/httpapi/web/static/reference/brand.svg" width="96" alt="Card Word 标志">
</p>
<h1 align="center">Card Word Community</h1>
<p align="center"><strong>简体中文</strong> · <a href="./docs/README.en.md">English</a></p>

可自部署的单店铺自动发卡系统。Go 单二进制、SQLite 数据库、内嵌前后台，无需 Node.js、PHP 或 MySQL。前台和后台沿用 Card Word 原版界面，默认使用经典商店首页，也可在「店铺装修」选择其他内置模板。

本目录是独立 Go 项目，可单独复制到新仓库。构建、测试和运行均在本目录内执行，不依赖上级目录。

## 功能

- 商品、分类、多规格、卡密库存、优惠码、订单查询和自动发货。
- 买家中心、管理员团队与角色权限、双因素认证、审计日志。
- 支付宝、微信支付、易支付及人工收款；每个实例使用店主自己的支付配置。
- 站点设置、公告、SEO、Markdown 编辑和店铺装修。
- 应用商城、上传安装、签名验证、插件启停与权限检查。

不包含多商家入驻、店铺目录、租户调度、平台审核、实名与短信入驻、代收结算、提现、保证金、平台投诉仲裁或应用发布中心。这些功能的服务端代码、页面和数据库表均不在本版本中。

## 应用商城

默认索引：`https://zinvmeng.com/market/index.json`。

后台「应用商城」直接读取此索引的 `plugins` 数据，不混入本地发布源或演示应用。下载必须通过 HTTPS，并验证索引中的 SHA-256；启用强制签名时，还需配置受信任的 Ed25519 公钥。应用安装后默认停用。远程源故障不影响已安装应用的管理。

可通过 `CARDWORD_MARKET_URL` 指定其他兼容源。索引为空时商城正常展示空状态，站点管理员仍可上传安装 ZIP。

## 启动

需要 Go 1.27.3 或兼容的后续版本：

```bash
go build -trimpath -o cardword ./cmd/cardword
./cardword --addr 127.0.0.1:8080 --base-url http://127.0.0.1:8080 --data ./data
```

Windows 使用 `go build -o cardword.exe ./cmd/cardword`，再运行 `./cardword.exe`，参数相同。

访问 `/install`，读取 `data/install-token.txt` 中的安装码，创建店主账号。安装完成后访问 `/admin`。不会预置公共管理员密码或演示库存。

线上部署把 `--base-url` 改为自己的 HTTPS 域名，并配置反向代理。配置与备份说明见 [部署文档](docs/DEPLOYMENT.md)。宝塔可使用 [Go 项目部署](deploy/baota-panel/README.md) 或 [systemd 部署](deploy/baota/README.md)。

## Docker

修改 `docker-compose.yml` 的 `CARDWORD_BASE_URL`，然后：

```bash
docker compose up -d --build
docker compose exec cardword cat /data/install-token.txt
```

首次访问 `/install` 创建管理员。持久化数据使用命名卷 `cardword-data`。

## 验证与发布

```bash
go test ./... -count=1
go vet ./...
python scripts/package_source.py
python scripts/build_baota.py --panel --version 0.1.0-community
```

源码包在 `dist/cardword-community-source.zip`；宝塔包在 `dist/releases/`。打包脚本只收集明确列出的源码、静态资源和部署文件，不包含本地运行数据、密钥或原平台版源码。

请使用新的空数据目录部署。这里不提供多租户数据库降级或平台数据迁移工具。

## 授权

本目录按 [Apache License 2.0](LICENSE) 开源，保留 Card Word 原有署名。第三方依赖与品牌标识的权利归其各自权利人；详见 [NOTICE](NOTICE)。上级目录的平台版不属于本开源发行包。

## 免责声明

本项目提供自部署店铺软件。每家店铺由其运营者独立管理；项目维护者不参与店铺交易，也不代收款或处理退款。运营者应确认有权销售所上架的商品，妥善保管买家数据和支付密钥，并负责交付与售后。买家如遇订单问题，请联系下单店铺。

软件按“现状”提供，担保与责任限制以 [Apache License 2.0 第 7、8 条](LICENSE) 为准。本说明不改变许可证授予的权利。
