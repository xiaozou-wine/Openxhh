# Openxhh

Openxhh 是一个面向小黑盒的 AI 自动回复机器人。它不是只会看见一句 `@机器人` 的关键词脚本，而是尽量把帖子、楼层、图片、上下文和被点名的人一起读进去，再用 OpenAI 兼容接口生成更像真人接话的回复。

> 推荐部署方式：**VPS / Linux 版本**。  
> Windows 图形化版本目前仍不完善，不建议普通用户优先安装；如果只是想稳定运行，请直接看 VPS 安装。

## 它解决了什么问题

普通机器人最大的问题是“眼睛太短”：用户说“这个怎么样”“楼上说得对吗”“给小菲画一张”，如果机器人只看到当前这一句话，就很容易答非所问。

Openxhh 的目标是让机器人更像一个真的在看帖的人：

- 它会读帖子标题和正文，而不是只看 @ 这一句。
- 它会利用当前评论楼层上下文，理解“楼上”“对方”“这个”指什么。
- 它会把评论图片、帖子图片作为上下文的一部分，让回复更贴合现场。
- 它支持评论区生图，并且不会再把 `已生成：prompt` 这种生硬文本直接丢到评论区。
- 它会清洗显式点名和小黑盒表情控制字符，尽量避免把 `[cube_喜欢]` 之类内容误当用户名。
- 它带 VPS Web UI，可以看最近对话、失败记录、token 消耗和日志，不用一直盯着终端。

## 主要功能

- 自动检查所有 @ 消息，并调用 OpenAI 兼容接口回复；也可手动开启白名单。
- 支持限制最高回复线程，避免后台一次性并发过多。
- 支持自定义 AI 接口、模型、token 和 prompt。
- 支持 SQLite / PostgreSQL；个人部署推荐 SQLite。
- 支持小黑盒扫码登录，登录态保存到 `cookie.json`。
- 支持帖子 / 评论楼层上下文增强。
- 支持评论图片和帖子图片进入 AI 上下文。
- 支持评论区 @ 生图：`生图`、`画图`、`生成图片`。
- 支持把生成图片写入 VPS 静态图床，并用 `imgs=<图片URL>` 发布顶级带图评论。
- 支持生图后生成自然短回复，避免暴露 prompt。
- 支持显式点名 @：例如“给小菲画一张”“问问楼主”“对小菲说”。
- 支持 VPS Web UI：服务控制、日志管理、最近对话、失败记录、token 统计。

## 最简单安装：VPS / Linux 推荐版

适合 Ubuntu / Debian VPS。下面只安装 **Openxhh 项目版本**，不包含旧仓库、不包含原版机器人。

```bash
sudo mkdir -p /opt/Openxhh
curl -fsSL https://raw.githubusercontent.com/Www8881313/Openxhh/main/scripts/update-installed.sh | sudo bash
cd /opt/Openxhh
sudo ./Openxhh
sudo nano config.json
sudo ./Openxhh -mode login
sudo ./Openxhh -mode start
```

这段命令会做几件事：

1. 把运行目录放到 `/opt/Openxhh`。
2. 从 `Www8881313/Openxhh` 拉取最新源码。
3. 编译 `Openxhh` 主程序。
4. 编译 `Openxhh-webui` VPS 控制台。
5. 保留你的 `config.json`、`cookie.json`、`sql.db` 和日志。
6. 生成配置文件、扫码登录，然后前台启动机器人。

如果你只是想先跑起来，看到这里就够了。下面都是更完整的配置、后台运行和生图说明。

<details>
<summary>配置文件示例</summary>

首次运行：

```bash
cd /opt/Openxhh
sudo ./Openxhh
```

程序会生成 `config.json` 并退出。编辑它：

```bash
sudo nano /opt/Openxhh/config.json
```

个人部署推荐 SQLite：

```json
{
  "xhh": {
    "checkTime": 60,
    "replyTime": 30,
    "maxReplyThreads": 3,
    "enableWhitelist": false,
    "owner": "你的 owner 数字UID；开启白名单时也作为允许 UID，多个用英文逗号分隔",
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
  },
  "image": {
    "model": "gpt-image-2",
    "baseUrl": "你的 OpenAI 兼容 /v1/images/generations 地址",
    "token": "你的图片 API Token",
    "size": "1024x1024",
    "responseFormat": "b64_json",
    "outputDir": "images",
    "uploadMode": "external",
    "externalDir": "/var/www/xhh-images",
    "externalBaseUrl": "http://你的VPS公网IP/xhh-images",
    "promptRefine": false,
    "promptModel": "",
    "promptBaseUrl": "",
    "promptToken": "",
    "promptMaxChars": 0
  }
}
```

配置要点：

- 默认 `xhh.enableWhitelist=false`，机器人会回复所有 @；需要只回复 owner / 指定用户时，改为 `true`。
- `xhh.owner` 填小黑盒数字 UID，不是昵称；多个 UID 用英文逗号分隔。即使白名单关闭，机器人也会用它识别 owner 身份。
- 开启白名单后，`xhh.owner` 会自动作为允许回复的 UID 列表，不需要额外重复添加。
- `xhh.maxReplyThreads` 控制同一轮最多并发回复数，个人部署建议保持 `3` 或更低。
- `ai.baseUrl` 要填完整的 Chat Completions 地址，例如 `/v1/chat/completions`。
- `image.baseUrl` 要填完整的 Images Generations 地址，例如 `/v1/images/generations`。
- `image.uploadMode=external` 是当前推荐方案，会把图片写入 `image.externalDir`，评论里使用 `image.externalBaseUrl`。
- `promptRefine=true` 后，可以用文本模型先把用户口语化生图请求整理成更适合图片模型的提示词。
- 不要公开 `config.json`、`cookie.json`、`sql.db`。

`v0.1.3` 及之后版本会自动补齐 `xhh.baseUrl`、`xhh.webver`、`xhh.version`、`database.type` 等默认值，但排查问题时仍建议确认这些字段存在。

</details>

<details>
<summary>扫码登录和前台试运行</summary>

扫码登录：

```bash
cd /opt/Openxhh
sudo ./Openxhh -mode login
```

扫码成功后会生成：

```text
/opt/Openxhh/cookie.json
```

前台试运行：

```bash
cd /opt/Openxhh
sudo ./Openxhh -mode start
```

确认可以正常收取 @、回复和写日志后，再配置 systemd 后台运行。

</details>

<details>
<summary>systemd 后台运行</summary>

创建机器人服务：

```bash
sudo tee /etc/systemd/system/Openxhh.service >/dev/null <<'EOF'
[Unit]
Description=Openxhh
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=/opt/Openxhh
ExecStart=/opt/Openxhh/Openxhh -mode start
Restart=always
RestartSec=10

[Install]
WantedBy=multi-user.target
EOF

sudo systemctl daemon-reload
sudo systemctl enable --now Openxhh
```

常用命令：

```bash
sudo systemctl start Openxhh
sudo systemctl stop Openxhh
sudo systemctl restart Openxhh
sudo systemctl status Openxhh --no-pager
sudo journalctl -u Openxhh -f
```

</details>

<details>
<summary>VPS Web UI 控制台</summary>

安装脚本会同时编译并更新：

```text
/opt/Openxhh/Openxhh-webui
```

推荐把它作为独立服务运行：

```bash
sudo tee /etc/systemd/system/Openxhh-webui.service >/dev/null <<'EOF'
[Unit]
Description=Openxhh VPS Web UI
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=/opt/Openxhh
ExecStart=/opt/Openxhh/Openxhh-webui -addr :29173 -root /opt/Openxhh -service Openxhh
Restart=always
RestartSec=10

[Install]
WantedBy=multi-user.target
EOF

sudo systemctl daemon-reload
sudo systemctl enable --now Openxhh-webui
sudo journalctl -u Openxhh-webui -n 50 --no-pager
```

第一次启动会生成随机强密码，日志里会出现：

```text
Openxhh VPS Web UI 已生成随机强密码
登录密码: xxxxxxxx
```

访问：

```text
http://你的VPS公网IP:29173
```

VPS Web UI 当前重点是运维面板，不是 Windows 那种安装向导。它适合用来：

- 查看 Openxhh 服务是否运行；
- 启动、停止、重启机器人服务；
- 查看最近 20 次对话；
- 查看 AI 失败回复；
- 查看总 token、最近 1 小时 token、最近 24 小时 token；
- 管理日志，支持复制选中、复制全部、暂停刷新、多行选择复制。

公网使用建议：

- 只在云安全组放行你自己的 IP；
- 不要把 `29173` 端口裸奔给所有人；
- 保存好第一次生成的随机密码；
- 需要重置密码时，停止 Web UI 后删除 `/opt/Openxhh/webui_auth.json` 再启动。

</details>

<details>
<summary>更新到最新版本</summary>

以后更新仍然只需要执行：

```bash
curl -fsSL https://raw.githubusercontent.com/Www8881313/Openxhh/main/scripts/update-installed.sh | sudo bash
```

默认会更新：

- `/opt/Openxhh/Openxhh`
- `/opt/Openxhh/Openxhh-webui`

并尝试重启：

- `Openxhh`
- `Openxhh-webui`

如果你的安装目录或服务名不同，可以这样指定：

```bash
curl -fsSL https://raw.githubusercontent.com/Www8881313/Openxhh/main/scripts/update-installed.sh | sudo env INSTALL_DIR=/你的安装目录 SERVICE_NAME=你的机器人服务名 WEBUI_SERVICE_NAME=你的WebUI服务名 bash
```

如果只想更新主程序，不更新 VPS Web UI：

```bash
curl -fsSL https://raw.githubusercontent.com/Www8881313/Openxhh/main/scripts/update-installed.sh | sudo env INSTALL_WEBUI=0 bash
```

</details>

## 评论区生图

Openxhh 支持在评论区直接 @ 机器人生图，例如：

```text
@机器人 生图 一只穿赛博朋克外套的猫，站在霓虹街头
@机器人 给小菲画一张雨夜里的机甲少女
```

它会尽量把生图请求整理成适合图片模型的 prompt，同时把回复文案处理得更自然：

- 不再直接输出 `已生成：prompt`。
- AI 短回复异常时会降级成 `图片来了喵`。
- 如果生图命令点名了目标用户，会同时 @ 触发者和目标用户。
- 会按 `data-user-id` 去重，避免重复 @。
- 会清洗 `[cube_*]` 等小黑盒表情控制字符。

<details>
<summary>配置外部静态图床</summary>

当前推荐使用 `image.uploadMode=external`：生成图片后写入 VPS 静态目录，再把公网图片 URL 发到小黑盒评论。

Nginx 示例：

```nginx
location /xhh-images/ {
    alias /var/www/xhh-images/;
    add_header Access-Control-Allow-Origin *;
}
```

准备目录并放入测试图：

```bash
sudo mkdir -p /var/www/xhh-images
sudo cp /你的测试图片.png /var/www/xhh-images/test.png
sudo chmod 755 /var/www/xhh-images
sudo chmod 644 /var/www/xhh-images/test.png
sudo nginx -t && sudo systemctl reload nginx
```

确认公网可访问：

```bash
curl -I http://你的VPS公网IP/xhh-images/test.png
```

</details>

<details>
<summary>生图验证命令</summary>

验证命令识别和 Form Data，不调用真实生图接口：

```bash
go run ./cmd/dry_run_image_comment \
  -comment_id 123 \
  -link_id 181099114 \
  -root_id 123 \
  -userid 你的ownerUID \
  -text "@机器人 生图 一只赛博朋克猫"
```

调用真实生图接口但不上传、不发评论：

```bash
go run ./cmd/dry_run_image_comment \
  -comment_id 123 \
  -link_id 181099114 \
  -root_id 123 \
  -userid 你的ownerUID \
  -text "@机器人 生图 一只赛博朋克猫" \
  -mock_image=false
```

验证已有图片 URL 能否发带图评论：

```bash
go run ./cmd/test_image_comment 181099114 "图片测试" "http://你的VPS公网IP/xhh-images/test.png"
```

验证本地图片上传到外部图床并可选发布评论：

```bash
go run ./cmd/test_xhh_image_upload_comment \
  -file ./images/example.png \
  -link_id 181099114 \
  -reply_id -1 \
  -root_id -1 \
  -text "图片测试" \
  -publish=true
```

</details>

## 为什么推荐 VPS 版

VPS 版本更适合长期运行，因为它的核心需求是“稳定在线”：

- 不依赖桌面窗口是否开着；
- 可以用 systemd 自动拉起；
- 日志、数据库、cookie 都在固定目录；
- 更新脚本能直接覆盖主程序和 VPS Web UI；
- Web UI 能远程看状态、看 token、复制日志、定位失败；
- 出问题时更容易用 `journalctl` 排查。

如果你只是想让机器人一直在小黑盒里工作，VPS 是主线版本。

## Windows 版本：目前不建议优先安装

Windows 图形化安装包还在完善中。它适合测试本地桌面控制台，但不建议作为主要部署方式。

如果你只是普通使用，请优先使用上面的 VPS 版本。Windows 版本可能遇到：

- 桌面窗口 / WebView2 环境差异；
- 扫码登录和本地控制台兼容性问题；
- 长期后台运行不如 VPS 稳定；
- 日志和服务管理不如 Linux systemd 清晰。

<details>
<summary>仍然想测试 Windows 版</summary>

可以下载 Release 中的：

```text
Openxhh-Setup-x64.exe
```

安装后会放置：

```text
C:\Program Files\Openxhh\Openxhh.exe
C:\Program Files\Openxhh\Openxhh-webui.exe
C:\ProgramData\Openxhh\config.json
C:\ProgramData\Openxhh\cookie.json
C:\ProgramData\Openxhh\sql.db
C:\ProgramData\Openxhh\log\
```

第一次使用：

1. 启动 Openxhh 控制台。
2. 保存页面显示的随机控制台密码，并登录本地控制台。
3. 在配置向导中填写 owner UID、AI 接口、模型和 token；如需只回复 owner / 指定用户，再开启白名单。
4. 点击“扫码登录”，使用小黑盒 App 扫码。
5. 日志提示 Cookie 已保存后，点击“启动”。

更多说明见 [docs/windows.md](docs/windows.md)。

</details>

## 安全建议

- `config.json` 包含 AI token。
- `cookie.json` 是小黑盒登录态。
- `sql.db` 里可能包含运行记录。
- 不要把这些文件上传到 GitHub。
- 不要把 `checkTime` 和 `replyTime` 调得太低，容易触发平台风控；建议保持 `checkTime=60`、`replyTime=30` 或更保守。
- VPS Web UI 不要全网裸奔，至少用云安全组限制来源 IP。

建议限制运行目录权限：

```bash
sudo chmod 600 /opt/Openxhh/config.json /opt/Openxhh/cookie.json /opt/Openxhh/sql.db 2>/dev/null || true
sudo chmod 700 /opt/Openxhh
```

## 回滚

更新脚本会自动备份旧二进制，文件名类似：

```text
/opt/Openxhh/Openxhh.bak-20260517-120000
/opt/Openxhh/Openxhh-webui.bak-20260517-120000
```

如需回滚主程序：

```bash
sudo systemctl stop Openxhh
sudo cp /opt/Openxhh/Openxhh.bak-时间戳 /opt/Openxhh/Openxhh
sudo chmod +x /opt/Openxhh/Openxhh
sudo systemctl start Openxhh
```

如需回滚 VPS Web UI：

```bash
sudo systemctl stop Openxhh-webui
sudo cp /opt/Openxhh/Openxhh-webui.bak-时间戳 /opt/Openxhh/Openxhh-webui
sudo chmod +x /opt/Openxhh/Openxhh-webui
sudo systemctl start Openxhh-webui
```

## 免责声明

本项目仅供个人学习和自用。自动化访问、自动回复、自动生图和频繁请求都可能触发平台风控。请自行控制频率，并遵守小黑盒相关规则。

## 致谢

感谢 [SomeOvO/xhhRobot](https://github.com/SomeOvO/xhhRobot) 原项目提供早期基础思路与实现参考。
