# 作为系统服务运行（Windows / Linux）

可执行文件使用 **[kardianos/service](https://github.com/kardianos/service)** 与 Windows 服务 (SCM)、Linux systemd（等）对接。启动时会 **`chdir` 到程序所在目录**，保证相对路径的 `config/`、`.env`、`trendradar.db`、`logs/` 在「服务/开机无登录」场景下仍与手动双击运行一致。

> **说明**
> - **「任务栏 / 托盘」**：本机后台 **HTTP 服务** 不会出现托盘图标。任务栏/托盘是 **GUI 应用** 的行为；本程序是**无界面服务**。**开机自启**请用下面的 **Windows 服务** 或 **Linux systemd**，二者可在未登录时启动（受系统策略/服务配置影响）。
> - **与 `sc` 的关系**：程序提供 `install` 等子命令，内部会注册到 SCM；你也可用 **`sc create`** 等命令手工注册，需自行指定 `binPath=` 与工作目录，一般直接用 **`管理员 CMD` 下执行内建子命令**即可。

## Windows

1. 将 `trendradar.exe` 及 `config/` 等整目录放到目标路径（例如 `D:\App\TrendRadar\`）。
2. **以管理员身份**打开 **命令提示符** 或 PowerShell，`cd` 到该目录。
3. 安装并启动服务（服务名 **`TrendRadar`**，与 `cmd/main.go` 中 `Name` 一致）：

```bat
trendradar.exe install
trendradar.exe start
```

4. 常用（也可用 `sc` / `net` 控制同一服务名）：

```bat
trendradar.exe stop
trendradar.exe restart
trendradar.exe uninstall
```

5. 在 **「服务」**（`services.msc`）中找到 **「TrendRadar 趋势雷达」**，可设 **启动类型 = 自动（延迟启动）** 等。开机后服务由系统拉启，**不是** 任务栏程序。

6. 查看日志：见 `config` 中 `logging.file`（如 `logs/trendradar.log`）。

7. 若 `install` 报权限或路径错误，请确认 **管理员** 且当前目录为包含 `config\config.yaml` 的部署目录。

## Linux（systemd 为主）

1. 将二进制与 `config/` 放到同目录，例如 `/opt/trendradar/`，并 `chmod +x trendradar`。
2. **root**（或具备 systemd 管理权限）执行：

```bash
cd /opt/trendradar
./trendradar install
./trendradar start
```

3. 开机自启一般配合 systemd：

```bash
sudo systemctl enable TrendRadar
sudo systemctl status TrendRadar
```

4. 卸载：

```bash
./trendradar stop
./trendradar uninstall
```

> 各发行版服务名/单元名与 kardianos 生成的一致（多为 `Name` 字段，即 `TrendRadar`）。以 `systemctl list-units | grep -i trend` 为准。

## 前台模式（开发调试）

不带子命令即与原先一致：

```bash
./trendradar
# Ctrl+C 会触发优雅停止（同旧版）
```

## 子命令速查

| 子命令     | 说明         |
|------------|--------------|
| `help`     | 简短帮助     |
| `install`  | 向系统登记服务 |
| `uninstall`| 从系统删除服务 |
| `start`    | 启动         |
| `stop`     | 停止         |
| `restart`  | 重启         |

## 与「启动文件夹 / 用户登录自启」的区别

- **本方案**：**系统服务**，通常 **先于用户登录** 运行，无界面，通过 **SC / systemd** 管理，适合 7×24 后端 API。
- **若必须「登录后、任务栏/托盘有图标」**：需要 **单独的 GUI/托盘小程序** 去拉启或监控本服务，**不在本 Go 后端的职责内**；可用任务计划（用户登录时）或第三方托盘包装器（如 NSSM 仅作进程守护时也无托盘）。
