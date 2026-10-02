# 开工入口 —— PortTool

**这份文件是给 AI 会话看的。**

这个仓库装 **`PortTool`**：给硬件工程师的端口测试面板（Go），以及它的主机侧测试、测试方案、工装固件的构建和交付打包。

> **产品文档在 `OpenPLC_Docs`**（`$PROD`），入口它的 `README.md`。工装固件的源码在 `$BOOT/TestCase/porttool/`。

## 这个仓库自己的东西

| 在哪 | 是什么 |
|---|---|
| `cmd/porttool/` | `PortTool`：双击开面板，带子命令（`ports` / `validate` / `run` / `version`）走命令行 |
| `internal/ptproto/` | `pt.*` 协议解析：四类行分流、`pt.caps`、采样帧、tick 回绕，以及哪些帧要在控制口回显（`Caps.EchoCommand`）。**面板和 CLI 共用同一份，两边判据不会分叉** |
| `internal/ptboard/` | 一条串口连接的对话管理：**永不停止地读**（停读就丢帧，这条链没有重传）、命令逐条串行（协议没有请求 id）、环形缓冲 + 订阅；`Lost()` 报串口断了 |
| `internal/ptpanel/` | 面板的 HTTP 面 + `go:embed` 的页面。推送用 **SSE 不是 WebSocket**（标准库，零依赖）。`Open` 字段是给测试注入假板子的缝。持续测试里的故障、复位、断线怎么体现见 `$PROD/docs/modules/M4/PORTTOOL-FLOW.md` C.3.1 |
| `internal/ptcheck/` | **限值与读数比对的唯一去处**。面板和产线序列都调它，两边不可能对同一个数得出不同结论 |
| `internal/ptplan/` | 方案文件（JSON）的读写与校验。**限值是步骤自己的参数，没有独立的限值表**（`$PROD/docs/tables/DECISIONS.md` 第 24 条）。`CheckAgainstCaps` 报只有板子能定的事；面板和 CLI 遇到它报出问题都拒跑（决策 77） |
| `internal/ptseq/` | 执行器。**六个通用字段的语义住在这里**：`execute_condition` 的门、重试、前后延时、超时。⚠️ 门看的是「上一个真正跑过的步骤」，所以被跳过的步骤不会把前面的失败洗掉。`FrameLookup` / `ReplyLookup` 是读数怎么取的唯一出处，面板判定也用它们 |
| `internal/ptreport/` | 报告。三条规矩：**每次尝试都留**（重试不覆盖原失败）、**原始值都留**（限值会改，要能重判）、**超时与判定失败分开记**（前者多半是接线/探针，后者多半是板子） |
| `internal/ptecho/` | loop=link 的对端（eth / usb / rs485）：**按字节原样送回**。面板和方案步骤用的是同一份（决策 77）；`FindCDC` 找板子的 USB CDC 口 |
| `internal/ptcal/` | 校准点的直线拟合（增益 + 偏移），面板 `/api/fit` 用 |
| `internal/calstore/` | 校准结果按板子 UID 存档到 exe 旁边的 `calibration/`，四路都合格时写出扇区 15 校准值区的镜像 `calarea.bin`。不往板子写 flash（CAL-04）。格式见 `$PROD/docs/modules/M4/PORTTOOL-FLOW.md` C.3.2 |
| `internal/portmap/` | 哪个 COM 口是哪个，记在 exe 旁边的 `porttool_ports.json`；面板和 `porttool run` 都读 |
| `internal/serialx/` | 串口层：开口、枚举（带 VID/PID）。macOS 上不开 cgo，VID/PID 由系统自带的 `ioreg` 补 —— 见 `enum_darwin.go` |
| `internal/simboard/` | 模拟板：找到并启动 `TestCase/host/porttool_caps/harness/` 下编出来的主机版固件；`OpenPort` 是「`sim` 还是真串口」的唯一判断 |
| `internal/calarea/` | 工装写进 flash `0x081E0000` 的校准值区。格式归 bootloader 的 `IAPServer/calib_area.h`；三方（bootloader、板卡包、本仓）一致由 `OpenPLC_Test` 的 P2 查（决策 78） |
| `TestCase/host/porttool_caps/` | 用例 **T4-01**：用真的 `porttool.c` 编主机版，再拿 Go 解析器对它的输出 |
| `TestCase/host/porttool_plan/` | 方案执行器和方案页的测试 |
| `TestCase/host/porttool_panel/` | 用例 **T4-02** / **T4-03**：浏览器里点面板 |
| `TestCase/plans/` | 随 PortTool 发出去的测试方案 |
| `TestCase/tools/` | `common.py`（本机路径、输出、工具链）、`init_machine.py`（生成 `TestCase/config/machine.py`，gitignored）、`build_fixture.py`（编工装镜像）、`selfcheck.py`、`make_delivery.py` + `md2html.py`（交付包） |

## 开工前

```
cd TestCase
python tools/init_machine.py       # 第一次：探测本机路径，写 config/machine.py
python tools/selfcheck.py          # 所有不需要板子的检查；--quick 跳过浏览器那步
```

本仓唯一依赖的另一个仓是 `$BOOT`（工装固件源码和 CubeIDE 工程），`init_machine.py` 在本仓旁边找它。

## 构建

| 目标 | 命令 |
|---|---|
| **编 / 烧 / 交付（菜单）** | **双击 `build.cmd`**，或者 `python build.py`。`--fixture` / `--tool` / `--flash` / `--deliver` 可组合。⚠️ 编固件前 CubeIDE 要关掉 |
| **出一版给硬件工程师** | **双击 `delivery.cmd`** —— 编固件 + 编 PortTool + 打包。产物在 `Output/delivery/`，整个文件夹发给他；他那边只要装 STM32CubeProgrammer |
| `PortTool`（三平台） | `./compile_tool.sh` —— 三平台各一个 amd64，外加 `Output/darwin-arm64/PortTool`，输出布局见 `$PROD/maps/porttool-on-linux-and-macos/issues/XPT-02-whether-to-ship-arm64.md` |

⚠️ 编完工装固件，`$BOOT/Debug/` 里就是工装镜像，不是 bootloader。

## 面板上的字一律说大白话

**用户 2026-09-11 定：「所有提示信息都要用大白话来说明。」**

⚠️ **而且是中文** —— 用户 2026-09-11 第二次指出：**「页面上英文的地方没有翻译成中文。」** 面板上**人看的字一处英文都不留**：标题、按钮、参数名、下拉选项、读数标签、提示、错误。唯一的例外是**协议原文的回显**（日志窗里的 `pt.start dout ...`、帧原文）—— 那是给对机器的，照原样显示，但它旁边必须有中文说明。

（这一条只管面板。代码注释、`#error` 文案、工具的 stdout/stderr 仍然是英文，见 `~/.claude/rules/language.md`。）

`PortTool` 面板的受众是**硬件工程师**，不是写这套协议的人。所以面板上出现的每一句话：

| 不要 | 要 |
|---|---|
| 直接抄协议字面量（`loop=ctrl`、`mode=extloop`、`mv=1:500`） | 说清它是什么意思、要他做什么 |
| 英文标签（`duty`、`freq`、`hold`、`seq/rx/miss`、`Klemmblock`） | 中文（`占空比`、`频率`、`保持`、`发/收/丢`、`端子排`） |
| 端口内部名（`dout3`、`rs4851`） | 工程师嘴里的名字（`DO3`、`RS485`），出处见 `Hardware/Klemmenbezeichnungen-R.pdf` |
| 裸错误码（`err=0x10000000`） | 译成原因，原始码可以跟在后面 |
| 只给数字 | 带单位、带范围、带"这算过还是不过" |

⚠️ **哪些数字不是判据，要在面板上说出来** —— 例如 `loop=ctrl` 端口回报的那三个计数只说明控制口活着，不是该端口的结论（`$PROD/docs/tables/DECISIONS.md` 第 9 条）。
