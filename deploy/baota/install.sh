#!/usr/bin/env bash
set -euo pipefail
umask 077

die() {
	printf '错误：%s\n' "$*" >&2
	exit 1
}
[[ ${EUID} -eq 0 ]] || die '请在宝塔终端中以 root 身份运行 bash install.sh。'
[[ $(uname -s) == Linux ]] || die '此部署包仅支持 Linux。'
[[ -d /run/systemd/system ]] || die '需要 systemd 服务管理器。'
for cmd in systemctl install useradd getent curl sha256sum ss tar sed flock; do command -v "$cmd" >/dev/null || die "缺少命令：$cmd"; done
case "$(uname -m)" in
x86_64) arch=amd64 ;;
aarch64 | arm64) arch=arm64 ;;
*) die '仅支持 x86_64 或 ARM64。' ;;
esac
src=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
for target in /opt/cardword /var/lib/cardword /etc/cardword /etc/systemd/system/cardword.service; do
	[[ ! -e "$target" && ! -L "$target" ]] || die "发现已有路径 $target；为保护数据已停止。更新现有站点请按部署说明操作。"
done
getent passwd cardword >/dev/null && die 'cardword 用户已存在，请先检查是否有旧安装。'
getent group cardword >/dev/null && die 'cardword 用户组已存在，请先检查是否有旧安装。'
(cd "$src" && sha256sum -c SHA256SUMS) || die '部署包校验失败。'
[[ -f "$src/bin/linux-$arch/cardword" ]] || die '缺少当前架构程序。'

printf 'Card Word 宝塔部署\n请先在宝塔创建站点并启用 HTTPS。无需 PHP、MySQL 或 Go 环境。\n'
read -r -p '站点完整地址（例如 https://shop.example.com）：' base_url
base_url=${base_url%/}
[[ "$base_url" =~ ^https://[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?(:[0-9]{1,5})?$ ]] || die '请输入完整 HTTPS 域名，不带目录、查询参数或账号密码；国际域名请使用 Punycode。'
read -r -p '本机监听端口 [8766]：' port
port=${port:-8766}
[[ "$port" =~ ^[1-9][0-9]{3,4}$ ]] && ((port >= 1024 && port <= 65535)) || die '端口应为 1024–65535。'
[[ -z $(ss -H -ltn "sport = :$port") ]] || die "端口 $port 已被占用。"

useradd --system --user-group --no-create-home --home-dir /var/lib/cardword --shell /usr/sbin/nologin cardword
install -d -m 0755 /opt/cardword
install -d -m 0700 -o cardword -g cardword /var/lib/cardword
install -d -m 0700 /etc/cardword
install -m 0755 "$src/bin/linux-$arch/cardword" /opt/cardword/cardword
install -m 0700 "$src/backup.sh" /opt/cardword/backup.sh
cat >/etc/cardword/cardword.env <<EOF
CARDWORD_ENV=production
CARDWORD_ADDR=127.0.0.1:$port
CARDWORD_BASE_URL=$base_url
CARDWORD_DATA_DIR=/var/lib/cardword
CARDWORD_TRUSTED_PROXIES=127.0.0.1,::1
CARDWORD_MAX_BODY_BYTES=16777216
CARDWORD_DB_READ_POOL=4
CARDWORD_LOG_LEVEL=info
EOF
chmod 0600 /etc/cardword/cardword.env
install -m 0644 "$src/cardword.service" /etc/systemd/system/cardword.service
sed "s/127.0.0.1:8766/127.0.0.1:$port/g" "$src/nginx-location.conf" >/etc/cardword/nginx-location.conf
chmod 0600 /etc/cardword/nginx-location.conf
systemctl daemon-reload
systemctl enable --now cardword.service
healthy=0
for ((i = 0; i < 30; i++)); do
	if curl --noproxy '*' -fsS --max-time 2 "http://127.0.0.1:$port/healthz" >/dev/null && [[ -s /var/lib/cardword/install-token.txt ]]; then
		healthy=1
		break
	fi
	sleep 1
done
((healthy == 1)) || die '程序启动未完成。请运行 journalctl -u cardword -n 80 --no-pager 检查日志；不要删除数据目录。'
printf '\n服务已启动。下一步：\n1. 在宝塔站点 Nginx 配置中添加 /etc/cardword/nginx-location.conf 的内容（替换原 location /）。\n2. 保存配置并重载 Nginx。\n3. 访问 %s/install 完成网页安装。\n4. 用 cat /var/lib/cardword/install-token.txt 读取安装码。\n\n仅代理到本机端口 %s，无需开放该端口到公网。\n' "$base_url" "$port"
