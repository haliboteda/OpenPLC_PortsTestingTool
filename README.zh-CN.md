# OpenPLC_PortsTestingTool

`PortTool`：OpenPLC 板子的端口测试面板。硬件工程师在浏览器里打开它，
逐个驱动跑着测试固件的板子上的端口，得到一份过 / 不过的报告。

English: [README.md](README.md)

## 从源码编译

只需要 Go 1.23 或更新的版本。第一次编译要下载模块，所以要能上网。在仓库根目录运行：

```
go build -o PortTool ./src/cmd/porttool       # 本机用的
```

在 Windows 上把输出文件命名为 `PortTool.exe`。要给别的系统编译，设置 `GOOS` / `GOARCH`（不需要 C 编译器）：

```
GOOS=linux   GOARCH=amd64 CGO_ENABLED=0 go build -o PortTool ./src/cmd/porttool
GOOS=darwin  GOARCH=arm64 go build -o PortTool ./src/cmd/porttool      # Apple 芯片
GOOS=windows GOARCH=amd64 go build -o PortTool.exe ./src/cmd/porttool
```

网页已经编进可执行文件，测试方案没有：把 `src/plans/*.json`
拷到 `PortTool` 旁边的 `plans/` 文件夹里。

在 Linux 上，运行 PortTool 的用户要有串口的访问权限，通常是加入 `dialout` 组。

## 全部编译、打交付包

`tools/build.sh` 是维护者用的入口，只编 PortTool 用不到它。它在仓库根目录运行 `go run ./tools/ptbuild`，
所以要装 Go，Windows 上还要 Git Bash。不带参数出菜单，带参数直接跑那一项。
先跑一次 `tools/build.sh find-tools`：它找出下面这些工具，写进 `tools/local.json`。
每一项开始前都查它要的工具，缺哪个就说出来。

| 命令 | 做什么 |
|---|---|
| `tools/build.sh find-tools` | 找本机工具，写 `tools/local.json` |
| `tools/build.sh tool [--os windows\|linux\|darwin\|darwin-arm64\|all]` | 编 PortTool，放进 `Output/bin/<平台>/`，旁边拷好 `plans/` |
| `tools/build.sh fixture` | 编测试固件，放进 `Output/fixture/` |
| `tools/build.sh flash` | 经 SWD 把这份固件烧进板子 |
| `tools/build.sh sim [--os ...]` | 编模拟板，放在 PortTool 旁边，没有板子也能用面板（端口选 `sim`） |
| `tools/build.sh deliver` | 打交付文件夹 `Output/delivery/`（Windows 和 Linux 在同一个文件夹） |
| `tools/build.sh all` | 编固件、编所有平台的 PortTool，再打交付文件夹 |

打包还要本仓旁边有：

| 要什么 | 干什么用 |
|---|---|
| `open_plc_cube_ide` | 测试固件的源码；它的 `Debug/open_plc_cube_ide.hex`（在 STM32CubeIDE 里编出的 bootloader）进 `stage6/` |
| STM32CubeIDE | 无界面编测试固件 |
| `IAPTranfer_Tool`，并已编出 `Output/windows/IAPTool.exe` 和 `Output/linux/IAPTool` | `stage6/` 里的上传工具 |
| `open_plc_arduino` | 编第 6 步 demo 用的 FQBN |
| gcc 或 clang（Windows 上用 MinGW） | 编本机的模拟板 |
| zig | 交叉编别的系统的模拟板 |
| arduino-cli，并装好 OpenPLC 板卡包 | 编第 6 步的 demo `FixtureDemo` |

包里有什么见 `OpenPLC_Docs` 的 `docs/modules/M4/PORTTOOL-FLOW.md` C.4。
