# Windows 图形化安装说明



## 推荐方式



下载 Release 里的 `Openxhh-Setup-x64.exe`，双击安装即可。安装过程会自动放置：



- 主程序：`C:\Program Files\Openxhh\Openxhh.exe`

- 控制台：`C:\Program Files\Openxhh\Openxhh-webui.exe`

- 数据目录：`C:\ProgramData\Openxhh`



数据目录会保存 `config.json`、`cookie.json`、`sql.db` 和 `log\`，卸载程序时默认保留，避免误删配置和登录态。



## 第一次使用



1. 安装完成后勾选“启动 Openxhh 控制台”，或双击桌面快捷方式。

2. 浏览器会自动打开本地控制台。

3. 首次启动页面会显示随机控制台密码，先保存，再登录。

4. 在“配置向导”里填写小黑盒 UID、AI 接口、模型和 token，点击“保存配置”。

5. 点击“扫码登录”，在左侧二维码卡片扫码；日志区域可查看登录进度。

6. 日志提示 Cookie 已保存后，点击“启动”。



## 开机自启



控制台左侧可以勾选“开机自启控制台”。它只写入当前用户的 Windows Run 启动项，不安装系统服务，适合小白使用。



## 手动运行



如果不使用安装包，也可以把两个 exe 放在同一目录，然后运行：



```powershell

Openxhh-webui.exe -root "C:\ProgramData\Openxhh" -bin "C:\Program Files\Openxhh\Openxhh.exe" -open-browser

```



## 构建安装包



Windows CI 会自动生成：



- `dist/windows/Openxhh.exe`

- `dist/windows/Openxhh-webui.exe`

- `dist/installer/Openxhh-Setup-x64.exe`



本地构建需要 Go、MinGW-w64 和 Inno Setup。普通用户不需要安装这些依赖，直接下载 setup 即可。
