# OpenPLC_PortsTestingTool

`PortTool`：OpenPLC 板子的端口测试面板。硬件工程师在浏览器里打开它，
逐个驱动跑着测试固件的板子上的端口，得到一份过 / 不过的报告。

English: [README.md](README.md)

## 从源码编译

只需要 Go 1.23 或更新的版本。第一次编译要下载模块，所以要能上网。在仓库根目录运行：

```
go build -o PortTool ./cmd/porttool       # 本机用的
```

在 Windows 上把输出文件命名为 `PortTool.exe`。要给别的系统编译，设置 `GOOS` / `GOARCH`（不需要 C 编译器）：

```
GOOS=linux   GOARCH=amd64 CGO_ENABLED=0 go build -o PortTool ./cmd/porttool
GOOS=darwin  GOARCH=arm64 go build -o PortTool ./cmd/porttool      # Apple 芯片
GOOS=windows GOARCH=amd64 go build -o PortTool.exe ./cmd/porttool
```

网页已经编进可执行文件，测试方案没有：把 `TestCase/plans/*.json`
拷到 `PortTool` 旁边的 `plans/` 文件夹里。

在 Linux 上，运行 PortTool 的用户要有串口的访问权限，通常是加入 `dialout` 组。

`compile_tool.sh`、`build.py` 和 `delivery.cmd` 是维护者用的脚本：它们还会编测试固件、
打包交付文件夹，需要固件仓库 `open_plc_cube_ide` 放在本仓旁边，还要装 STM32CubeIDE。
先跑一次 `python TestCase/tools/init_machine.py` 记下它们在哪。只编 PortTool 用不到它们。
