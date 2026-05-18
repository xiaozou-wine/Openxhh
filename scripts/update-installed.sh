#!/usr/bin/env bash

set -euo pipefail



REPO_URL="${REPO_URL:-https://github.com/Www8881313/Openxhh.git}"

BRANCH="${BRANCH:-main}"

INSTALL_DIR="${INSTALL_DIR:-/opt/Openxhh}"

SERVICE_NAME="${SERVICE_NAME:-Openxhh}"

WEBUI_SERVICE_NAME="${WEBUI_SERVICE_NAME:-Openxhh-webui}"

WEBUI_BIN_NAME="${WEBUI_BIN_NAME:-Openxhh-webui}"

INSTALL_WEBUI="${INSTALL_WEBUI:-${INSTALL_WEBUI_VPS:-1}}"

GOPROXY_VALUE="${GOPROXY:-https://goproxy.cn,direct}"

GOSUMDB_VALUE="${GOSUMDB:-sum.golang.google.cn}"

GO_BUILD_P="${GO_BUILD_P:-1}"



log() {

  printf '[Openxhh update] %s\n' "$*"

}



need_root() {

  if [ "$(id -u)" -ne 0 ]; then

    log "请使用 root 运行，或使用：curl -fsSL <脚本地址> | sudo bash"

    exit 1

  fi

}



install_build_deps() {

  if command -v git >/dev/null 2>&1 && command -v go >/dev/null 2>&1 && command -v gcc >/dev/null 2>&1; then

    return

  fi



  if ! command -v apt-get >/dev/null 2>&1; then

    log "未检测到 apt-get，请手动安装 git、Go、gcc、sqlite3 开发库后重试。"

    exit 1

  fi



  log "安装构建依赖：git、curl、gcc、sqlite3 dev、snapd。"

  apt-get update

  apt-get install -y git curl ca-certificates build-essential libsqlite3-dev snapd



  if ! command -v go >/dev/null 2>&1; then

    log "安装 Go。"

    systemctl enable --now snapd.socket >/dev/null 2>&1 || true

    snap install go --classic

  fi

}



main() {

  need_root



  if [ ! -d "$INSTALL_DIR" ]; then

    log "安装目录不存在：$INSTALL_DIR"

    log "如果你的安装目录不同，请设置 INSTALL_DIR，例如：INSTALL_DIR=/path/to/Openxhh bash update-installed.sh"

    exit 1

  fi



  if [ ! -f "$INSTALL_DIR/config.json" ]; then

    log "警告：$INSTALL_DIR/config.json 不存在。更新会继续，但程序可能无法启动。"

  fi



  install_build_deps



  tmp_dir="$(mktemp -d)"

  trap 'rm -rf "$tmp_dir"' EXIT



  log "拉取源码：$REPO_URL ($BRANCH)"

  git clone --depth 1 --branch "$BRANCH" "$REPO_URL" "$tmp_dir/src"



  log "编译二进制。"

  cd "$tmp_dir/src"

  export GOPROXY="$GOPROXY_VALUE"

  export GOSUMDB="$GOSUMDB_VALUE"

  export GOMAXPROCS="${GOMAXPROCS:-1}"

  export CGO_ENABLED=1

  go mod download

  go build -p "$GO_BUILD_P" -o "$tmp_dir/Openxhh" .

  if [ "$INSTALL_WEBUI" != "0" ]; then

    go build -p "$GO_BUILD_P" -o "$tmp_dir/$WEBUI_BIN_NAME" ./cmd/webui-vps

  fi



  timestamp="$(date +%Y%m%d-%H%M%S)"

  service_exists=0

  webui_service_exists=0

  if systemctl cat "$SERVICE_NAME" >/dev/null 2>&1; then

    service_exists=1

  fi

  if [ "$INSTALL_WEBUI" != "0" ] && systemctl cat "$WEBUI_SERVICE_NAME" >/dev/null 2>&1; then

    webui_service_exists=1

  fi



  if [ "$service_exists" -eq 1 ]; then

    log "停止服务：$SERVICE_NAME"

    systemctl stop "$SERVICE_NAME" || true

  fi

  if [ "$webui_service_exists" -eq 1 ]; then

    log "停止 Web UI 服务：$WEBUI_SERVICE_NAME"

    systemctl stop "$WEBUI_SERVICE_NAME" || true

  fi



  if [ -f "$INSTALL_DIR/Openxhh" ]; then

    log "备份旧二进制：$INSTALL_DIR/Openxhh.bak-$timestamp"

    cp "$INSTALL_DIR/Openxhh" "$INSTALL_DIR/Openxhh.bak-$timestamp"

  fi

  if [ "$INSTALL_WEBUI" != "0" ] && [ -f "$INSTALL_DIR/$WEBUI_BIN_NAME" ]; then

    log "备份旧 Web UI 二进制：$INSTALL_DIR/$WEBUI_BIN_NAME.bak-$timestamp"

    cp "$INSTALL_DIR/$WEBUI_BIN_NAME" "$INSTALL_DIR/$WEBUI_BIN_NAME.bak-$timestamp"

  fi



  log "替换二进制，保留 config.json、cookie.json、sql.db 和 log。"

  cp "$tmp_dir/Openxhh" "$INSTALL_DIR/Openxhh"

  chmod +x "$INSTALL_DIR/Openxhh"

  if [ "$INSTALL_WEBUI" != "0" ]; then

    cp "$tmp_dir/$WEBUI_BIN_NAME" "$INSTALL_DIR/$WEBUI_BIN_NAME"

    chmod +x "$INSTALL_DIR/$WEBUI_BIN_NAME"

  fi



  if [ "$service_exists" -eq 1 ]; then

    log "启动服务：$SERVICE_NAME"

    systemctl start "$SERVICE_NAME"

    systemctl status "$SERVICE_NAME" --no-pager || true

  else

    log "未检测到 systemd 服务 $SERVICE_NAME。你可以手动运行：cd $INSTALL_DIR && ./Openxhh -mode start"

  fi

  if [ "$INSTALL_WEBUI" != "0" ]; then

    if [ "$webui_service_exists" -eq 1 ]; then

      log "启动 Web UI 服务：$WEBUI_SERVICE_NAME"

      systemctl start "$WEBUI_SERVICE_NAME"

      systemctl status "$WEBUI_SERVICE_NAME" --no-pager || true

    else

      log "已更新 Web UI 二进制：$INSTALL_DIR/$WEBUI_BIN_NAME"

    fi

  fi



  log "更新完成。"

}



main "$@"
