#!/usr/bin/env bash
set -euo pipefail
umask 077

# BaoTa owns the process lifecycle. Keep this process in the foreground.
project_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
if [[ $# -lt 1 || $# -gt 2 ]]; then
	echo '用法：bash start.sh https://你的域名 [端口，默认8766]' >&2
	exit 1
fi
base_url=${1%/}
port=${2:-8766}
[[ "$base_url" =~ ^https://[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?(:[0-9]{1,5})?$ ]] || {
	echo '请填写 HTTPS 域名，不带目录或查询参数。' >&2
	exit 1
}
[[ "$port" =~ ^[1-9][0-9]{3,4}$ ]] && ((port >= 1024 && port <= 65535)) || {
	echo '端口应为 1024–65535。' >&2
	exit 1
}
[[ -x "$project_dir/cardword" ]] || {
	echo '请在宝塔文件管理中将 cardword 文件权限设为 755。' >&2
	exit 1
}
export CARDWORD_TRUSTED_PROXIES=127.0.0.1,::1
export CARDWORD_MAX_BODY_BYTES=16777216
export CARDWORD_DB_READ_POOL=4
exec "$project_dir/cardword" --addr "127.0.0.1:$port" --base-url "$base_url" --data "$project_dir/data" --env production
