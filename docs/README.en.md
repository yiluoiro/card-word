<p align="center">
  <img src="../internal/httpapi/web/static/reference/brand.svg" width="96" alt="Card Word logo">
</p>
<h1 align="center">Card Word Community</h1>
<p align="center"><a href="../README.md">简体中文</a> · <strong>English</strong></p>

Card Word Community is a self-hosted shop for selling digital goods and delivering codes after payment. The storefront and admin console are embedded in a single Go binary backed by SQLite. No Node.js, PHP, or MySQL is required. The classic Card Word storefront is the default; the owner can choose another built-in design in the admin console.

## Features

- Manage products, categories, variants, code inventory, and coupons. Buyers can look up their orders and retrieve their codes.
- Provide buyer accounts and manage staff access with roles, two-factor authentication, and audit logs.
- Accept Alipay, WeChat Pay, EPay, or manually confirmed payments using the store’s own payment settings.
- Customize the storefront, publish announcements, edit Markdown, and configure search indexing.
- Browse the app marketplace, upload ZIP packages, verify signatures, review permissions, and enable or disable plugins.

Each instance runs one shop. This edition omits the store directory, merchant onboarding and verification, multi-tenant routing, platform reviews, centralized settlement, withdrawals, deposits, dispute arbitration, and app publishing. The corresponding server code, pages, and database tables are excluded.

## App marketplace

The admin console reads the `plugins` list from `https://zinvmeng.com/market/index.json`. It does not add demo listings. Package downloads require HTTPS and must match the SHA-256 digest in the index. If you require signatures, configure the publisher’s trusted Ed25519 public key. Newly installed plugins are disabled until you enable them.

Set `CARDWORD_MARKET_URL` to use another compatible index. An empty index shows an empty catalog. If the source is unavailable, you can still manage installed plugins or upload a ZIP package.

## Getting started

From the repository root, build with Go 1.27.3 or a compatible later version:

```bash
go build -trimpath -o cardword ./cmd/cardword
./cardword --addr 127.0.0.1:8080 --base-url http://127.0.0.1:8080 --data ./data
```

On Windows, build with `go build -o cardword.exe ./cmd/cardword`, then run `./cardword.exe` with the same arguments. Start with an empty data directory; this edition does not include a migration tool for multi-tenant databases.

Open `http://127.0.0.1:8080/install` and enter the token from `data/install-token.txt` to create the owner account. Then sign in at `/admin`. The installer does not create a shared default password or demo inventory.

For production, set `--base-url` to your public HTTPS origin and configure a reverse proxy. The [deployment and backup guide](DEPLOYMENT.md), [BaoTa panel guide](../deploy/baota-panel/README.md), and [systemd guide](../deploy/baota/README.md) are in Chinese.

## Docker

Set `CARDWORD_BASE_URL` in `docker-compose.yml` to the address buyers will use, then run:

```bash
docker compose up -d --build
docker compose exec cardword cat /data/install-token.txt
```

Use the token at `/install` to create the owner account. The `cardword-data` volume stores the shop’s data.

## Testing and packaging

**Admin updates (v1.0.0 onward).** Each admin page checks for a stable GitHub release; click the version in the top bar to refresh immediately. The dialog shows release notes. Only the store owner can change the trusted public GitHub repository, install an update, or restart the shop. Installing downloads the executable for this OS and architecture and verifies its GitHub SHA-256 digest. The current version runs until the owner selects **Restart to apply**. The launcher drains requests, closes the database, and starts the new worker with the original arguments. If startup fails before the database and listener are ready, the previous executable starts again.

Install v1.0.0 manually once when upgrading from a version without the updater. Keep the data volume across container replacements. Its `updates/` directory must allow the service account to write and execute files. Back up before upgrading: executable fallback does not reverse database migrations.

To publish an update, push a tested stable tag such as `v1.0.1`. The release workflow publishes standalone Linux, Windows, and macOS binaries (amd64/arm64). Write notes in `docs/releases/<tag>.md` or use the automatically generated GitHub notes.

```bash
go test ./... -count=1
go vet ./...
python scripts/package_source.py
python scripts/build_baota.py --panel --version v1.0.0
```

The source archive is written to `dist/cardword-community-source.zip`; BaoTa packages are written to `dist/releases/`. The packaging scripts exclude local shop data, secret keys, and proprietary platform code.

## License

Card Word Community is distributed under the [Apache License 2.0](../LICENSE). Third-party dependencies and brand marks retain their respective rights; see [NOTICE](../NOTICE). The platform edition is a separate project and is not part of this distribution.

## Disclaimer

Each store is independently operated. The project maintainers do not participate in store transactions, collect payments, or process refunds. Store operators are responsible for having the right to sell their products, safeguarding buyer data and payment credentials, delivering purchases, and providing support. Buyers with an order issue should contact the store where they placed the order.

The software is provided “as is.” Sections 7 and 8 of the [Apache License 2.0](../LICENSE) set out the disclaimer of warranty and limitation of liability. This notice does not change the rights granted by the license.
