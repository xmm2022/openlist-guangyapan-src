#!/usr/bin/env bash
set -euo pipefail

APP_NAME="${APP_NAME:-openlist-guangyapan}"
SERVICE_NAME="${SERVICE_NAME:-openlist-guangyapan}"
HTTP_PORT="${HTTP_PORT:-9876}"
REPO_URL="${REPO_URL:-https://github.com/xmm2022/openlist-guangyapan-src.git}"
BRANCH="${BRANCH:-main}"
INSTALL_DIR="${INSTALL_DIR:-/opt/openlist-guangyapan}"
SRC_DIR="${SRC_DIR:-${INSTALL_DIR}/src}"
DATA_DIR="${DATA_DIR:-${INSTALL_DIR}/data}"
GO_BIN="${GO_BIN:-go}"

BIN_PATH="${INSTALL_DIR}/${APP_NAME}"
CONFIG_PATH="${DATA_DIR}/config.json"
UNIT_PATH="/etc/systemd/system/${SERVICE_NAME}.service"

die() {
  echo "error: $*" >&2
  exit 1
}

need_cmd() {
  command -v "$1" >/dev/null 2>&1 || die "missing required command: $1"
}

if [ "$(id -u)" -ne 0 ]; then
  die "please run as root, for example: curl -fsSL <script-url> | sudo bash"
fi

case "${HTTP_PORT}" in
  ''|*[!0-9]*)
    die "HTTP_PORT must be a number"
    ;;
esac

if [ "${HTTP_PORT}" -lt 1 ] || [ "${HTTP_PORT}" -gt 65535 ]; then
  die "HTTP_PORT must be between 1 and 65535"
fi

need_cmd git
need_cmd systemctl
need_cmd "${GO_BIN}"

mkdir -p "${INSTALL_DIR}" "${DATA_DIR}"

if [ -f "./go.mod" ] && grep -q "module github.com/OpenListTeam/OpenList/v4" "./go.mod"; then
  BUILD_DIR="$(pwd)"
  echo "Using current source tree: ${BUILD_DIR}"
else
  if [ -d "${SRC_DIR}/.git" ]; then
    echo "Updating source tree: ${SRC_DIR}"
    git -C "${SRC_DIR}" fetch --prune origin
    git -C "${SRC_DIR}" checkout "${BRANCH}"
    git -C "${SRC_DIR}" reset --hard "origin/${BRANCH}"
  else
    echo "Cloning ${REPO_URL} (${BRANCH}) to ${SRC_DIR}"
    rm -rf "${SRC_DIR}"
    git clone --branch "${BRANCH}" "${REPO_URL}" "${SRC_DIR}"
  fi
  BUILD_DIR="${SRC_DIR}"
fi

echo "Building ${APP_NAME}"
tmp_bin="$(mktemp)"
cleanup() {
  rm -f "${tmp_bin}"
}
trap cleanup EXIT

(
  cd "${BUILD_DIR}"
  "${GO_BIN}" build -tags=jsoniter -o "${tmp_bin}" .
)
install -m 0755 "${tmp_bin}" "${BIN_PATH}"

if [ ! -f "${CONFIG_PATH}" ]; then
  cat >"${CONFIG_PATH}" <<JSON
{
  "scheme": {
    "address": "0.0.0.0",
    "http_port": ${HTTP_PORT},
    "https_port": -1
  }
}
JSON
else
  config_tmp="$(mktemp)"
  config_tool_dir="$(mktemp -d)"
  config_tool="${config_tool_dir}/update-openlist-config.go"
  cat >"${config_tool}" <<'GO'
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
)

func main() {
	configPath := os.Getenv("CONFIG_PATH")
	port, err := strconv.Atoi(os.Getenv("HTTP_PORT"))
	if err != nil {
		panic(err)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		panic(err)
	}
	var cfg map[string]any
	if len(data) > 0 {
		if err := json.Unmarshal(data, &cfg); err != nil {
			panic(err)
		}
	}
	if cfg == nil {
		cfg = map[string]any{}
	}
	scheme, _ := cfg["scheme"].(map[string]any)
	if scheme == nil {
		scheme = map[string]any{}
		cfg["scheme"] = scheme
	}
	scheme["address"] = "0.0.0.0"
	scheme["http_port"] = port
	if _, ok := scheme["https_port"]; !ok {
		scheme["https_port"] = -1
	}
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		panic(err)
	}
	fmt.Println(string(out))
}
GO
  CONFIG_PATH="${CONFIG_PATH}" HTTP_PORT="${HTTP_PORT}" "${GO_BIN}" run "${config_tool}" >"${config_tmp}"
  cat "${config_tmp}" >"${CONFIG_PATH}"
  rm -f "${config_tmp}"
  rm -rf "${config_tool_dir}"
fi

cat >"${UNIT_PATH}" <<UNIT
[Unit]
Description=OpenList GuangYaPan CAS
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=${INSTALL_DIR}
ExecStart=${BIN_PATH} server --data ${DATA_DIR} --log-std
Restart=on-failure
RestartSec=3
Environment=TZ=Asia/Shanghai
Environment=UMASK=022

[Install]
WantedBy=multi-user.target
UNIT

systemctl daemon-reload
systemctl enable "${SERVICE_NAME}" >/dev/null
systemctl restart "${SERVICE_NAME}"

echo
echo "Deployment complete."
echo "Service: ${SERVICE_NAME}"
echo "Install dir: ${INSTALL_DIR}"
echo "Data dir: ${DATA_DIR}"
echo "URL: http://127.0.0.1:${HTTP_PORT}"
echo
systemctl --no-pager --full status "${SERVICE_NAME}" || true
