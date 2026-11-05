#!/usr/bin/env bash
set -euo pipefail
umask 077
[[ ${EUID} -eq 0 ]] || {
	echo '请以 root 身份执行。' >&2
	exit 1
}
for path in /var/lib/cardword /etc/cardword /opt/cardword/cardword /etc/systemd/system/cardword.service; do
	[[ -e "$path" ]] || {
		printf '缺少 %s\n' "$path" >&2
		exit 1
	}
done
command -v flock >/dev/null || {
	echo '需要 util-linux 提供的 flock。' >&2
	exit 1
}
exec 9>/run/lock/cardword-backup.lock
flock -n 9 || {
	echo '已有备份正在执行。' >&2
	exit 1
}
install -d -m 0700 /var/backups/cardword
archive="/var/backups/cardword/cardword-$(date -u +%Y%m%dT%H%M%SZ)-$$.tar.gz"
active=0
systemctl is-active --quiet cardword && active=1
restart() { if ((active == 1)); then systemctl start cardword; fi; }
trap restart EXIT
systemctl stop cardword
# Stopping the service also closes all tenant databases, avoiding inconsistent
# copies of SQLite WAL files. The master key is included with the databases.
tar -czf "$archive" -C / var/lib/cardword etc/cardword opt/cardword etc/systemd/system/cardword.service
chmod 0600 "$archive"
sha256sum "$archive" >"$archive.sha256"
printf '备份完成：%s\n' "$archive"
