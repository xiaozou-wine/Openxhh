# xhhRobot 增强版

基于 [SomeOvO/xhhRobot](https://github.com/SomeOvO/xhhRobot) 的小黑盒 AI 自动回复机器人增强版。

本版本重点增强 AI 回复时的上下文能力：在小黑盒接口返回评论楼层数据时，AI 不只看到帖子正文，还会看到当前评论楼层的文字上下文和评论图片，从而能更准确地理解「这个怎么样」「楼上说得对吗」这类问题。

## 功能

- 自动检查配置白名单用户的 @ 消息，并调用 OpenAI 兼容接口回复。
- 支持自定义 AI 接口、模型和 prompt。
- 支持 SQLite / PostgreSQL，个人部署建议使用 SQLite。
- 增强 AI 输入上下文：
  - 帖子标题和正文；
  - 帖子正文图片；
  - 当前评论楼层上下文；
  - 当前评论楼层里的图片，最多附带 4 张。
- 保留原版 `config.json`、`cookie.json`、`sql.db` 工作方式。

说明：评论楼层上下文依赖小黑盒接口返回的 `comments` 字段。部分帖子或楼层接口可能不返回评论区数据，这种情况下机器人仍会正常回复，但只能基于帖子正文和当前 @ 内容判断。

## 快速更新已安装的原版

如果你已经按原版方式部署在 `/opt/xhhRobot`，并且已有：

```text
/opt/xhhRobot/config.json
/opt/xhhRobot/cookie.json
/opt/xhhRobot/xhhRobot
```

可以直接执行：

```bash
curl -fsSL https://raw.githubusercontent.com/Www8881313/xhhRobot/main/scripts/update-installed.sh | sudo bash
```

脚本会自动完成：

1. 安装必要构建依赖；
2. 拉取本仓库源码；
3. 编译新的 `xhhRobot` 二进制；
4. 停止 `xhhRobot` systemd 服务，如果存在；
5. 备份旧二进制；
6. 替换为增强版二进制；
7. 保留原来的 `config.json`、`cookie.json`、`sql.db` 和日志；
8. 重新启动服务。

如果你的安装目录不是 `/opt/xhhRobot`，可以这样指定：

```bash
curl -fsSL https://raw.githubusercontent.com/Www8881313/xhhRobot/main/scripts/update-installed.sh | sudo env INSTALL_DIR=/你的安装目录 bash
```

如果你的 systemd 服务名不是 `xhhRobot`，可以这样指定：

```bash
curl -fsSL https://raw.githubusercontent.com/Www8881313/xhhRobot/main/scripts/update-installed.sh | sudo env SERVICE_NAME=你的服务名 bash
```

也可以先下载脚本再执行：

```bash
curl -fsSL -o update-installed.sh https://raw.githubusercontent.com/Www8881313/xhhRobot/main/scripts/update-installed.sh
sudo bash update-installed.sh
```

## 全新安装

### 1. 下载源码并编译

```bash
apt update
apt install -y git curl ca-certificates build-essential libsqlite3-dev snapd
systemctl enable --now snapd.socket
snap install go --classic

git clone https://github.com/Www8881313/xhhRobot /opt/xhhRobot-src
cd /opt/xhhRobot-src
export GOPROXY=https://goproxy.cn,direct
export GOSUMDB=sum.golang.google.cn
export GOMAXPROCS=1
go build -p 1 -o xhhRobot .
```

### 2. 准备运行目录

```bash
mkdir -p /opt/xhhRobot
cp /opt/xhhRobot-src/xhhRobot /opt/xhhRobot/xhhRobot
chmod +x /opt/xhhRobot/xhhRobot
cd /opt/xhhRobot
```

### 3. 生成配置文件

首次运行会生成 `config.json` 并退出：

```bash
./xhhRobot
```

编辑配置：

```bash
nano /opt/xhhRobot/config.json
```

推荐个人部署使用 SQLite：

```json
{
  "xhh": {
    "checkTime": 60,
    "replyTime": 30,
    "owner": "你的数字UID",
    "deviceID": "",
    "baseUrl": "https://api.xiaoheihe.cn",
    "webver": "2.5",
    "version": "999.0.4"
  },
  "database": {
    "type": "sqlite",
    "db": "",
    "host": "",
    "port": "",
    "user": "",
    "passwd": ""
  },
  "ai": {
    "model": "你的模型名",
    "prompt": "你的回复策略",
    "baseUrl": "你的 OpenAI 兼容 /v1/chat/completions 地址",
    "token": "你的 AI API Token"
  }
}
```

注意：

- `xhh.owner` 填小黑盒数字 UID，不是昵称。
- `ai.baseUrl` 要填完整的 Chat Completions 地址，例如 `/v1/chat/completions`。
- 不要公开 `config.json`、`cookie.json`、`sql.db`。

### 4. 登录小黑盒

```bash
cd /opt/xhhRobot
./xhhRobot -mode login
```

扫码成功后会生成：

```text
/opt/xhhRobot/cookie.json
```

### 5. 前台试跑

```bash
cd /opt/xhhRobot
./xhhRobot -mode start
```

如果确认正常，再配置 systemd。

### 6. systemd 后台运行

```bash
cat >/etc/systemd/system/xhhRobot.service <<'EOF'
[Unit]
Description=xhhRobot
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=/opt/xhhRobot
ExecStart=/opt/xhhRobot/xhhRobot -mode start
Restart=always
RestartSec=10

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable --now xhhRobot
```

查看状态和日志：

```bash
systemctl status xhhRobot --no-pager
journalctl -u xhhRobot -f
```

## 常用命令

```bash
systemctl start xhhRobot
systemctl stop xhhRobot
systemctl restart xhhRobot
systemctl status xhhRobot --no-pager
journalctl -u xhhRobot -f
```

## 安全建议

- `config.json` 包含 AI token，`cookie.json` 是小黑盒登录态，不要上传到 GitHub。
- 建议设置权限：

```bash
chmod 600 /opt/xhhRobot/config.json /opt/xhhRobot/cookie.json /opt/xhhRobot/sql.db 2>/dev/null || true
chmod 700 /opt/xhhRobot
```

- 不要把 `checkTime` 和 `replyTime` 调得太低，容易触发小黑盒风控。建议：

```json
"checkTime": 60,
"replyTime": 30
```

## 回滚

更新脚本会自动备份旧二进制，文件名类似：

```text
/opt/xhhRobot/xhhRobot.bak-20260517-120000
```

如需回滚：

```bash
systemctl stop xhhRobot
cp /opt/xhhRobot/xhhRobot.bak-时间戳 /opt/xhhRobot/xhhRobot
chmod +x /opt/xhhRobot/xhhRobot
systemctl start xhhRobot
```

## 免责声明

本项目仅供个人学习和自用。自动化访问、自动回复和频繁请求可能触发平台风控，请自行控制频率并遵守小黑盒相关规则。
