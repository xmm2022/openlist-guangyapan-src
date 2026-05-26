#!/usr/bin/env bash
set -euo pipefail

APP_NAME="${APP_NAME:-openlist-guangyapan}"
SERVICE_NAME="${SERVICE_NAME:-openlist-guangyapan}"
HTTP_PORT="${HTTP_PORT:-9876}"
REPO="${REPO:-xmm2022/openlist-guangyapan-src}"
VERSION="${VERSION:-latest}"
INSTALL_DIR="${INSTALL_DIR:-/opt/openlist-guangyapan}"
DATA_DIR="${DATA_DIR:-${INSTALL_DIR}/data}"
GH_PROXY="${GH_PROXY:-}"

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

need_cmd curl
need_cmd install
need_cmd systemctl
need_cmd tar

case "$(uname -s)" in
  Linux) ;;
  *) die "only Linux is supported" ;;
esac

case "$(uname -m)" in
  x86_64|amd64)
    ARCH="amd64"
    ;;
  aarch64|arm64)
    ARCH="arm64"
    ;;
  *)
    die "unsupported architecture: $(uname -m). Supported: x86_64, aarch64"
    ;;
esac

ASSET="${APP_NAME}_linux_${ARCH}.tar.gz"
if [ "${VERSION}" = "latest" ]; then
  DOWNLOAD_URL="${GH_PROXY}https://github.com/${REPO}/releases/latest/download/${ASSET}"
else
  DOWNLOAD_URL="${GH_PROXY}https://github.com/${REPO}/releases/download/${VERSION}/${ASSET}"
fi

tmp_dir="$(mktemp -d)"
cleanup() {
  rm -rf "${tmp_dir}"
}
trap cleanup EXIT

echo "Downloading ${DOWNLOAD_URL}"
curl -fL --retry 3 --retry-delay 2 -o "${tmp_dir}/${ASSET}" "${DOWNLOAD_URL}"

sha_url="${DOWNLOAD_URL}.sha256"
if command -v sha256sum >/dev/null 2>&1 && curl -fL --retry 3 --retry-delay 2 -o "${tmp_dir}/${ASSET}.sha256" "${sha_url}"; then
  (
    cd "${tmp_dir}"
    sha256sum -c "${ASSET}.sha256"
  )
else
  echo "Skipping sha256 verification"
fi

mkdir -p "${tmp_dir}/extract"
tar -xzf "${tmp_dir}/${ASSET}" -C "${tmp_dir}/extract"

if [ -f "${tmp_dir}/extract/${APP_NAME}" ]; then
  extracted_bin="${tmp_dir}/extract/${APP_NAME}"
elif [ -f "${tmp_dir}/extract/openlist" ]; then
  extracted_bin="${tmp_dir}/extract/openlist"
else
  extracted_bin="$(find "${tmp_dir}/extract" -maxdepth 2 -type f -perm -111 | head -n 1 || true)"
fi

if [ -z "${extracted_bin:-}" ] || [ ! -f "${extracted_bin}" ]; then
  die "downloaded archive does not contain an executable binary"
fi

mkdir -p "${INSTALL_DIR}" "${DATA_DIR}"
install -m 0755 "${extracted_bin}" "${BIN_PATH}"

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
Environment=OPENLIST_ADDR=0.0.0.0
Environment=OPENLIST_HTTP_PORT=${HTTP_PORT}

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
echo "Binary: ${BIN_PATH}"
echo "URL: http://127.0.0.1:${HTTP_PORT}"
echo
systemctl --no-pager --full status "${SERVICE_NAME}" || true
