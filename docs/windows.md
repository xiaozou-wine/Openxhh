# Windows 图形化安装说明

## 推荐方式

下载 Release 里的 `Openxhh-Setup-x64.exe`，双击安装即可。安装过程会自动放置：

- 主程序：`C:\Program Files\Openxhh\Openxhh.exe`
- 桌面控制台：`C:\Program Files\Openxhh\Openxhh-webui.exe`
- 数据目录：`C:\ProgramData\Openxhh`

数据目录会保存 `config.json`、`cookie.json`、`sql.db` 和 `log\`，卸载程序时默认保留，避免误删配置和登录态。

## 第一次使用

1. 安装完成后勾选“启动 Openxhh 控制台”，或双击桌面快捷方式。
2. Openxhh 会打开独立桌面窗口，不再弹出系统浏览器。
3. 首次启动页面会显示随机控制台密码，先保存，再登录。
4. 在“配置向导”里填写 owner UID、AI 接口、模型和 token；如需只回复 owner / 指定用户，再开启白名单，点击“保存配置”。
5. 点击“扫码登录”，在左侧二维码卡片扫码；日志区域可查看登录进度。
6. 日志提示 Cookie 已保存后，点击“启动”。

## 小黑盒默认配置

扫码登录依赖以下默认值，通常不要清空：

```json
{
  "xhh": {
    "baseUrl": "https://api.xiaoheihe.cn",
    "webver": "2.5",
    "version": "999.0.4"
  }
}
```

`v0.1.3` 及之后版本会自动补齐这些默认值，但排查问题时仍建议确认 `C:\ProgramData\Openxhh\config.json` 里存在它们。

## 开机自启

控制台左侧可以勾选“开机自启控制台”。它只写入当前用户的 Windows Run 启动项，不安装系统服务，适合小白使用。

## 调试模式

如果需要在浏览器里调试旧版 Web UI，可以手动运行：

```powershell
Openxhh-webui.exe -root "C:\ProgramData\Openxhh" -bin "C:\Program Files\Openxhh\Openxhh.exe" -browser
```

只启动本地服务、不打开窗口：

```powershell
Openxhh-webui.exe -root "C:\ProgramData\Openxhh" -bin "C:\Program Files\Openxhh\Openxhh.exe" -server-only
```

## WebView2 Runtime

桌面控制台依赖 Microsoft Edge WebView2 Runtime。Windows 10/11 通常已经自带；如果极少数系统缺失，启动桌面窗口失败时会自动回退到浏览器模式，后续可以手动安装 WebView2 Runtime 后再试。

## 构建安装包

Windows CI 会自动生成：

- `dist/windows/Openxhh.exe`
- `dist/windows/Openxhh-webui.exe`
- `dist/installer/Openxhh-Setup-x64.exe`

本地构建需要 Go、MinGW-w64、WebView2 构建依赖和 Inno Setup。普通用户不需要安装这些依赖，直接下载 setup 即可。
