#!/usr/bin/env bash
set -euo pipefail

SERVICE_NAME="${1:-trigger-admin-bot.service}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

GO_BIN="${GO_BIN:-}"
if [ -z "$GO_BIN" ]; then
	if [ -x /usr/local/go/bin/go ]; then
		GO_BIN=/usr/local/go/bin/go
	else
		GO_BIN="$(command -v go || true)"
	fi
fi

if [ -z "$GO_BIN" ] || [ ! -x "$GO_BIN" ]; then
	echo "ERROR: go binary not found. Set GO_BIN=/path/to/go." >&2
	exit 1
fi

exec_start="$(systemctl show "$SERVICE_NAME" -p ExecStart --value)"
exec_path="$(printf '%s\n' "$exec_start" | sed -n 's/.*path=\([^ ;]*\).*/\1/p' | head -n 1)"

if [ -z "$exec_path" ]; then
	exec_path="$(
		systemctl cat "$SERVICE_NAME" --no-pager |
			awk -F= '/^[[:space:]]*ExecStart=/{print $2; exit}' |
			awk '{print $1}'
	)"
fi

if [ -z "$exec_path" ] || [ "${exec_path#/}" = "$exec_path" ]; then
	echo "ERROR: cannot resolve absolute ExecStart path for $SERVICE_NAME." >&2
	exit 1
fi

case "$exec_path" in
	"$ROOT_DIR"/*) ;;
	*)
		echo "ERROR: refusing to build outside project root." >&2
		echo "  project:   $ROOT_DIR" >&2
		echo "  ExecStart: $exec_path" >&2
		exit 1
		;;
esac

GO_LIMIT_PROCS="${GO_LIMIT_PROCS:-1}"
GO_BUILD_P="${GO_BUILD_P:-1}"
PKGS="${PKGS:-./...}"

echo "== deploy $SERVICE_NAME =="
echo "root:      $ROOT_DIR"
echo "go:        $GO_BIN"
echo "ExecStart: $exec_path"
echo

cd "$ROOT_DIR"

if [ "${SKIP_TESTS:-0}" != "1" ]; then
	read -r -a test_pkgs <<< "$PKGS"
	echo "== go test =="
	GOMAXPROCS="$GO_LIMIT_PROCS" "$GO_BIN" test -p "$GO_BUILD_P" -count=1 "${test_pkgs[@]}"
	echo
fi

echo "== go build =="
GOMAXPROCS="$GO_LIMIT_PROCS" "$GO_BIN" build -p "$GO_BUILD_P" -o "$exec_path" .
stat -c 'built: %n %s bytes %y' "$exec_path"
echo

echo "== restart =="
sudo -n systemctl restart "$SERVICE_NAME"
sleep 1
systemctl is-active --quiet "$SERVICE_NAME"
systemctl status "$SERVICE_NAME" --no-pager -n 20
echo

echo "== recent logs =="
tmp_log="$(mktemp)"
trap 'rm -f "$tmp_log"' EXIT
if sudo -n journalctl -u "$SERVICE_NAME" --since '2 minutes ago' --no-pager >"$tmp_log" 2>/dev/null; then
	tail -n 80 "$tmp_log"
else
	echo "WARN: cannot read journal with sudo -n; service status above is still verified." >&2
fi
