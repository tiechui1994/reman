# Reman

Reman 是一个功能强大的进程管理器，用于管理和监控多个进程。它支持进程自动重启、依赖管理、日志记录、RPC 远程控制，以及作为系统服务运行。

## 功能特性

- ✅ **多进程管理**：同时管理多个进程，支持进程依赖关系
- ✅ **跨主机操作**：支持通过 `-H` 参数远程控制其他主机上的进程
- ✅ **自动重启**：支持 always、on-failure、never 三种重启策略
- ✅ **日志管理**：彩色日志输出，支持日志文件记录（仅在服务端运行模式下生成）
- ✅ **RPC 控制**：通过 RPC 接口远程控制进程，支持参数校验
- ✅ **系统服务**：支持安装为 Windows 和 Linux 系统服务
- ✅ **配置文件热重载**：支持运行时重新加载配置文件
- ✅ **文件监控**：自动监控可执行文件变化并重启进程
- ✅ **进程升级**：支持在线升级进程可执行文件，支持跨主机文件同步升级
- ✅ **跨平台**：支持 Windows 和 Unix/Linux 系统

## 安装

### 使用 Go 安装

```bash
go install github.com/tiechui1994/reman@latest
```

## 快速开始

### 1. 创建配置文件

创建 `Procfile.toml` 文件：

```toml
exit-on-stop = true
exit-on-error = false

[[procs]]
name = "web"
work-dir = "/path/to/app"
cmd-line = "python3 app.py"
restart = "always"
env = { PORT = "8080" }

[[procs]]
name = "worker"
work-dir = "/path/to/app"
cmd-line = "python3 worker.py"
restart = "on-failure"
depend = ["web"]
```

### 2. 启动进程

```bash
# 启动所有进程
reman start

# 启动指定进程
reman start web

# 检查配置文件
reman check
```

### 3. 通过 RPC 控制

```bash
# 查看进程状态
reman run list

# 停止进程
reman run stop web

# 重启进程
reman run restart web

# 重启所有进程（会重新加载配置文件）
reman run restart-all
```

## 配置文件说明

### Procfile.toml 格式

```toml
# 全局配置
exit-on-stop = true      # 所有进程停止时是否退出
exit-on-error = false    # 进程出错时是否退出

# 进程配置
[[procs]]
name = "进程名称"          # 必填：进程名称
work-dir = "/path/to"    # 可选：工作目录
cmd-line = "命令"         # 必填：启动命令
restart = "always"       # 可选：重启策略 (always/on-failure/never)
restart-max-retries = 100 # 可选：最大重启次数
env = { KEY = "value" } # 可选：环境变量（TOML 格式：KEY = "value"）
log = "/path/to/log"     # 可选：日志文件路径
is-show = false          # 可选：是否显示控制台输出
version = false          # 可选：是否显示版本信息
depend = ["proc1"]      # 可选：依赖的进程名称列表
```

### 重启策略

- `always`：进程退出后总是重启
- `on-failure`：仅在进程异常退出（非 0 退出码）时重启
- `never`：进程退出后不重启

### 进程依赖

使用 `depend` 字段指定进程依赖关系。被依赖的进程会先启动，确保启动顺序。

## 命令说明

### 基本命令

```bash
# 检查配置文件
reman check

# 显示帮助信息
reman help

# 显示版本信息
reman version

# 启动进程
reman start [PROCESS...]
```

### RPC 命令

```bash
# 启动进程 (需指定进程名)
reman run [-H host] start NAME...

# 停止进程 (需指定进程名)
reman run [-H host] stop NAME...

# 停止所有进程
reman run [-H host] stop-all

# 重启进程 (需指定进程名)
reman run [-H host] restart NAME...

# 重启所有进程（会重新加载配置文件）
reman run [-H host] restart-all

# 查看进程列表 (支持指定进程名过滤)
reman run [-H host] list [NAME...]

# 查看进程状态 (支持指定进程名过滤)
reman run [-H host] status [NAME...]

# 升级进程 (需指定进程名和路径)
reman run [-H host] upgrade NAME PATH

# 调试模式（启动 pprof）
reman run [-H host] debug
```

### 服务管理命令

```bash
# 安装为系统服务（需要管理员/root 权限）
reman install [-service SERVICE_NAME]

# 卸载系统服务（需要管理员/root 权限）
reman uninstall [-service SERVICE_NAME]
```

## 系统服务安装

### Windows

```powershell
# 以管理员身份运行 PowerShell
reman install -service MyService

# 启动服务
sc start MyService

# 停止服务
sc stop MyService

# 卸载服务
reman uninstall -service MyService
```

**注意**：
- 需要以管理员身份运行
- 安装前必须确保 `Procfile.toml` 文件存在
- 服务会自动启动，并在系统启动时自动运行

### Linux/Unix

```bash
# 使用 sudo 安装
sudo reman install -service myreman

# 启动服务
sudo systemctl start myreman

# 停止服务
sudo systemctl stop myreman

# 查看服务状态
sudo systemctl status myreman

# 卸载服务
sudo reman uninstall -service myreman
```

**注意**：
- 需要 root 权限（使用 sudo）
- 安装前必须确保 `Procfile.toml` 文件存在
- 服务会自动启用，并在系统启动时自动运行

## 命令行选项

```bash
-f, -procfile string     # 指定 Procfile 路径（默认：Procfile.toml）
-p, -port uint          # RPC 服务器端口（默认：18555）
-H, -host string         # 指定远程主机地址（用于 run 命令）
-basedir string         # 基础目录
-service string         # Windows 服务名称
-rpc-server             # 是否启动 RPC 服务器（默认：true）
-logtime                # 日志中显示时间戳（默认：true）
-h, --help               # 显示帮助信息
```

## 环境变量

- `REMAN_RPC_SERVER`：RPC 服务器地址
- `REMAN_RPC_PORT`：RPC 服务器端口
- `REMAN_RPC_ADDR`：RPC 服务器监听地址

## 配置文件示例

### 完整示例

```toml
exit-on-stop = true
exit-on-error = false

[[procs]]
name = "database"
work-dir = "/opt/database"
cmd-line = "mysqld --defaults-file=/opt/database/my.cnf"
restart = "always"
log = "/var/log/mysql.log"
env = { MYSQL_HOME = "/opt/database", MYSQL_PORT = "3306" }

[[procs]]
name = "api-server"
work-dir = "/opt/api"
cmd-line = "node server.js"
restart = "on-failure"
restart-max-retries = 10
depend = ["database"]
env = { NODE_ENV = "production", PORT = "3000" }

[[procs]]
name = "worker"
work-dir = "/opt/worker"
cmd-line = "python3 worker.py"
restart = "always"
depend = ["database", "api-server"]
log = "/var/log/worker.log"
```

## 日志管理

### 日志输出

- 每个进程都有独立的彩色日志标识
- 支持时间戳显示（可通过 `-logtime` 控制）
- 支持日志文件输出（通过 `log` 字段配置）

### 日志格式

```
[时间] 进程名 | 日志内容
```

## 进程升级

使用 `upgrade` 命令可以升级正在运行的进程：

```bash
# 从本地文件升级（本地运行）
reman run upgrade web /path/to/new/binary

# 跨主机升级（自动将本地文件同步至远程并升级）
reman run -H 192.168.1.100 upgrade web ./local_binary

# 从 URL 下载并升级
reman run upgrade web http://example.com/new/binary
```

跨主机升级特性：
- 当目标为远程主机时，客户端会自动启动一个临时的 HTTP 服务来分发本地文件。
- 升级过程会自动处理网络路径适配，确保远程主机可访问。
- 升级完成后临时服务会自动关闭。

升级流程：
1. 停止目标进程
2. 备份旧的可执行文件
3. 替换为新的可执行文件
4. 重新启动进程

## 常见问题

### Q: 如何查看进程运行状态？

A: 使用 `reman run list` 或 `reman run status` 命令。

### Q: 进程无法启动怎么办？

A: 
1. 检查配置文件是否正确：`reman check`
2. 检查工作目录和命令路径是否正确
3. 查看日志文件或控制台输出

### Q: 如何修改配置文件后生效？

A: 使用 `reman run restart-all` 命令，会自动重新加载配置文件。

### Q: 服务安装失败？

A: 
- Windows：确保以管理员身份运行
- Linux：确保使用 sudo 运行
- 确保 `Procfile.toml` 文件存在且格式正确

### Q: 如何设置进程依赖？

A: 在配置文件中使用 `depend` 字段，例如：
```toml
[[procs]]
name = "app"
depend = ["database", "redis"]
```

## 开发

### 构建

```bash
go build -o reman
```

## 许可证

MIT License

Copyright (c) 2024 Reman Contributors

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.

## 贡献

欢迎提交 Issue 和 Pull Request！

## 版本历史

- (Latest):
  - 增强跨主机升级：支持在无公网 IP (NAT) 环境下通过 RPC 直接推送二进制文件进行升级
  - 优化升级反馈：升级成功后会输出备份文件名及新版本信息 (需开启 version 选项)


- v0.3.17:
  - 增加跨主机操作支持：通过 `-H` 参数管理远程进程
  - 增强跨主机升级功能：支持本地文件自动同步升级远程对端
  - 优化日志创建逻辑：仅在服务端/服务模式下创建 `run.log`
  - 完善参数校验：为 `start`, `stop`, `restart`, `upgrade` 增加强制参数检查
  - 增强 `list`/`status`：支持按进程名称过滤显示
  - 统一帮助信息：支持 `-h`/`--help` 输出自定义使用说明

- v0.3.16:
  - 支持系统服务安装和卸载
  - 支持配置文件热重载
  - 改进错误处理和日志记录

