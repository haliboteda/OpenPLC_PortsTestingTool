# 开工入口 —— PortTool

**这份文件是给 AI 会话看的。**

这个仓库装 **`PortTool`**：给硬件工程师的端口测试面板（Go），以及它的主机侧测试、测试方案、工装固件的构建和交付打包。

> **产品文档在 `OpenPLC_Docs`**（`$PROD`），入口它的 `README.md`。工装固件的源码在 `$BOOT/TestCase/porttool/`。

## 四个目录（决策 149）

| 目录 | 放什么 |
|---|---|
| `src/` | 产品的全部源码：`src/cmd/porttool/`（入口）、`src/<包>/`（各包，单元测试贴着包放）、`src/plans/`（随包发的测试方案）、`src/simboard/c/`（模拟板的 C 外壳：`sim_main.c`、`stubs/`、`sources.txt`）。不用 `internal/`，因为 `TestCase/` 的测试要 import 这些包 |
| `tools/` | 发包要的全部东西：`build.sh`（唯一入口，菜单；仓根 `start.sh` 只转调它）、`ptbuild/`（Go：找本机工具、编 PortTool、编 / 烧工装固件、编模拟板、打交付包）、`localcfg/`（读 `local.json` 的唯一一处，发包工具和测试都 import 它）、`firmhost/`（固件主机版的源文件清单和编译参数，模拟板、T4-01、T4-05 共用）、`delivery/`（交付包的模板：`start.cmd`、`start.sh`、两份 README 正文、HTML 外壳）、`shipped_fixture.json`（上次打包的版本和哈希）、`local.json`（本机工具路径，gitignored） |
| `TestCase/` | 测试有关的全部东西：`test.sh`（菜单）、`host/` 下的 T4-01 / T4-02 / T4-04 / T4-05、T4-01 专用的 `test_main.c`、黄金文件、`tools/`（剩下的 Python 测试共用的 `common.py`、临时诊断 `aodiag.py`） |
| `Output/` | 产物（不进 git）：`bin/<windows\|linux\|darwin\|darwin-arm64>/`（PortTool、`plans/`、模拟板）、`fixture/`（工装镜像和 `VERSION`）、`delivery/`（交付包）、`build/`（中间产物） |

## 各包

| 在哪 | 是什么 |
|---|---|
| `src/cmd/porttool/` | `PortTool`：双击开面板，带子命令（`ports` / `validate` / `run` / `version`）走命令行 |
| `src/ptproto/` | `pt.*` 协议解析：四类行分流、`pt.caps`、`pt.cal`（板上校准值区，`ParseCal`）、`pt.calw`（烧校准值的命令行，`CalWriteCommand`）、采样帧、tick 回绕，以及哪些帧要在控制口回显（`Caps.EchoCommand`）。**面板和 CLI 共用同一份，两边判据不会分叉** |
| `src/ptboard/` | 一条串口连接的对话管理：**永不停止地读**（停读就丢帧，这条链没有重传）、命令逐条串行（协议没有请求 id）、环形缓冲 + 订阅；`Lost()` 报串口断了 |
| `src/ptpanel/` | 面板的 HTTP 面 + `go:embed` 的页面（`web/index.html` 只放骨架，脚本按块在 `web/js/`，布局见 `$PROD/docs/modules/M4/PORTTOOL-FLOW.md` C.3.6）。推送用 **SSE 不是 WebSocket**（标准库，零依赖）。`Open` 字段是给测试注入假板子的缝。工装完整测试的步号、结果和日志在 `journal.go`（决策 128）。持续测试里的故障、复位、断线怎么体现见 `$PROD/docs/modules/M4/PORTTOOL-FLOW.md` C.3.1 |
| `src/ptitems/` | 测试项目录：每个端口有哪些测试项、跑哪个固件目标、出现在哪几类测试、要什么对端、判据出自哪几步。面板 `/api/items` 按 `pt.caps` 过滤后用它画中间的端口和右边的卡片。见 `$PROD/docs/modules/M4/PORTTOOL-FLOW.md` C.3.6 |
| `src/ptcheck/` | **限值与读数比对的唯一去处**。面板和产线序列都调它，两边不可能对同一个数得出不同结论 |
| `src/ptplan/` | 方案文件（JSON）的读写与校验。**限值是步骤自己的参数，没有独立的限值表**（`$PROD/docs/tables/DECISIONS.md` 第 24 条）。`CheckAgainstCaps` 报只有板子能定的事；面板和 CLI 遇到它报出问题都拒跑（决策 77） |
| `src/ptseq/` | 执行器。**六个通用字段的语义住在这里**：`execute_condition` 的门、重试、前后延时、超时。⚠️ 门看的是「上一个真正跑过的步骤」，所以被跳过的步骤不会把前面的失败洗掉。`FrameLookup` / `ReplyLookup` 是读数怎么取的唯一出处，面板判定也用它们 |
| `src/ptlib/` | 面板右上角「开始」的排程：勾的测试项 → 判据方案的步骤 → `ptseq`；选了时长时先挨个跑只测一次和问人的项，其余同时跑到时长、第一次不过就停；端口结论（KNX 只记录的诊断不算）。见 `$PROD/docs/modules/M4/PORTTOOL-FLOW.md` C.3.6「测试项怎么跑」 |
| `src/ptreport/` | 报告。三条规矩：**每次尝试都留**（重试不覆盖原失败）、**原始值都留**（限值会改，要能重判）、**超时与判定失败分开记**（前者多半是接线/探针，后者多半是板子） |
| `src/ptecho/` | loop=link 的对端（eth / usb / rs485）：**按字节原样送回**。面板和方案步骤用的是同一份（决策 77）；`FindCDC` 找板子的 USB CDC 口 |
| `src/slcan/` | 上位机 CAN 对端：CANable 2.0 的 SLCAN 驱动和 `mode=echo` 式回送（决策 103）。见 `$PROD/docs/modules/M4/PORTTOOL-FLOW.md` C.3.7 |
| `src/knxusb/` | 上位机 KNX 对端：标准 KNX USB HID 接口（Transfer Protocol、Bus Access Server 选 EMI、cEMI / EMI2 / EMI1 的 L_Data），KNX 分析窗口用（决策 151）。HID 层 Windows 调系统 DLL、Linux 走 `/dev/hidraw`、macOS 经 purego 调 IOKit（不开 cgo，`$PROD/maps/porttool-on-linux-and-macos/issues/XPT-06-knx-usb-on-macos.md`）。见 `$PROD/docs/modules/M4/PORTTOOL-FLOW.md` C.3.7「上位机 KNX」 |
| `src/pthold/` | 板子看门狗 `pt.hold` 的武装、续期、解除；执行器陪跑标准机时用，面板持续运行（`ptpanel/hold.go`）用它的时长和命令 |
| `src/ptcal/` | 校准点的直线拟合（增益 + 偏移），面板 `/api/analog/fit` 用 |
| `src/calstore/` | 校准结果按板子 UID 存档到 exe 旁边的 `calibration/`，四路都有时写出扇区 15 校准值区的镜像 `calarea.bin`（残差不判，决策 99）；同一目录下的 `ranges.json` 是 AI / AO 档位登记（决策 102）；`spec.go` 是档位表：每档的跳线、标称公式、满量程，面板显示和执行器换算都从这里取（决策 136）。格式见 `$PROD/docs/modules/M4/PORTTOOL-FLOW.md` C.3.2 |
| `src/stlink/` | 经 `STM32_Programmer_CLI` 擦写板子：烧 bootloader、恢复出厂（烧校准值改由工装固件 `pt.calw`，决策 142）。调用命令行的那一层可以换成替身（`stlinktest`），主机测试不碰 ST-Link。做法见 `$PROD/docs/modules/M4/PORTTOOL-FLOW.md` C.3.5；`ListProbes`（`probe.go`）只读地查插没插探针，面板连接区用（C.3.6） |
| `src/ptstages/` | 工装完整测试的执行顺序：勾选的步依次跑、没通过就停、停下记 `STOPPED`、后面记 `SKIPPED`；第 6 步的「烧 bootloader → 上传 demo → 测 → 恢复出厂」。见 C.3.3、C.3.5 |
| `src/ptrecord/` | 完整测试的逐板记录 `records/<UID>/` 和续跑用的 `progress.json`。见 C.3.3 |
| `src/portmap/` | 哪个 COM 口是哪个，记在 exe 旁边的 `porttool_ports.json`；面板和 `porttool run` 都读 |
| `src/serialx/` | 串口层：开口、枚举（带 VID/PID）。macOS 上不开 cgo，VID/PID 由系统自带的 `ioreg` 补 —— 见 `enum_darwin.go` |
| `src/simboard/` | 模拟板：找到并启动编出来的主机版固件（先 exe 旁边，再往上找 `Output/bin/<本机平台>/`）；C 外壳在 `c/`，菜单「编模拟板」编它；`OpenPort` 是「`sim` 还是真串口」的唯一判断；`OpenPair` 起被测板和标准机两块，都是 `sim` 时按标准机的默认接法接线（`refboard.Default`，不跟着登记变），见 `$PROD/docs/modules/M4/PORTTOOL-FLOW.md` C.3.7 |
| `src/refboard/` | 标准机的登记：模拟量、数字量的接法（经过哪个继电器、接到标准机哪一路）和面板跑它用的参数，按标准机 UID 存 `refboard.json`；同一路接两次、继电器串两次拒存。面板「标准机设置」页和执行器都照它（决策 135，`$PROD/docs/modules/M4/PORTTOOL-FLOW.md` C.3.6「标准机设置页」） |
| `src/calarea/` | 工装写进 flash `0x081E0000` 的校准值区。格式归 bootloader 的 `IAPServer/calib_area.h`；三方（bootloader、板卡包、本仓）一致由 `OpenPLC_Test` 的 P2 查（决策 78） |
| `TestCase/host/porttool_caps/` | 用例 **T4-01**：用真的 `porttool.c` 编主机版，逐项查它的输出，再拿 Go 解析器对同一份字节。`go test -race ./TestCase/host/porttool_caps/ -args -build`（`TestCase/test.sh t4-01`）；不带 `-build` 只跑 Go 那半 |
| `TestCase/host/porttool_demo/` | 用例 **T4-05**：第 6 步的 demo（板卡包例程 `FixtureDemo`）和工装固件讲同一套协议，同一份 Go 测试对模拟板和 demo 的主机版各跑一遍。`go test ./TestCase/host/porttool_demo/ -args -build`（`TestCase/test.sh t4-05`），两块都编进 `Output/build/porttool_demo/`；不带 `-build` 跳过 |
| `TestCase/host/porttool_plan/` | 方案执行器、判据选择、完整测试和校准的面板接口测试 |
| `TestCase/host/porttool_panel/` | 用例 **T4-02** / **T4-03**：浏览器里点面板。Go 程序（chromedp 驱动 `tools/local.json` 的 `CHROME`），在仓根 `go run ./TestCase/host/porttool_panel --port sim`；`go test ./...` 只编译它 |
| `src/plans/` | 随 PortTool 发出去的测试方案 |
| `tools/ptbuild/` | 发包工具，`tools/build.sh` 不带参数出菜单，带参数直接跑（见下面「构建」） |

## 开工前

```
tools/build.sh find-tools     # 第一次：探测本机工具，写 tools/local.json
TestCase/test.sh              # 不需要板子的检查（菜单）；TestCase/test.sh all 全跑
```

Windows 上在 Git Bash 里运行这两个 `.sh`。本仓依赖的另一个仓是 `$BOOT`（工装固件源码和 CubeIDE 工程）；交付包另从 `$BOOT/Debug` 取 bootloader、从 `$TOOL/Output/windows` 和 `$TOOL/Output/linux` 取 IAPTool 给第 6 步（决策 103，只读）、从 `$CORE_REPO` 取编 demo 的 FQBN。「找本机工具」在本仓旁边找这几个仓。

## 构建

入口只有 `tools/build.sh`：不带参数出菜单，带参数直接跑（给测试和 CI 用）。仓根的 `start.sh` 只转调它，双击就出菜单（决策 149）。「全部」先清空整个 `Output/`，单项编译前只删自己那份产物（决策 149「清理」）。每一项开始前查它要的本机工具，缺的逐个列出、这一项不做。

| 菜单项 | 参数 | 产物 |
|---|---|---|
| 找本机工具 | `find-tools`（`--set KEY=VALUE`、`--check`） | `tools/local.json` |
| 编 PortTool | `tool [--os windows\|linux\|darwin\|darwin-arm64\|all]`（默认 `all`） | `Output/bin/<平台>/PortTool[.exe]`，旁边拷好 `plans/`；平台布局见 `$PROD/maps/porttool-on-linux-and-macos/issues/XPT-02-whether-to-ship-arm64.md` |
| 编工装固件 | `fixture` | `Output/fixture/`（镜像和 `VERSION`），顺带重编本机模拟板 |
| 烧工装固件（SWD） | `flash` | 把 `Output/fixture/` 的镜像烧进板子，要 ST-Link |
| 编模拟板 | `sim [--os ...]`（默认本机） | `Output/bin/<平台>/porttool_simboard[.exe]`；本机平台用 `HOST_CC`，别的平台用 zig 交叉编 |
| 打交付包 | `deliver [--open]` | `Output/delivery/`（Windows 和 Linux 同一个文件夹，Linux 上 `bash start.sh`），整个文件夹发给硬件工程师；他那边只要装 STM32CubeProgrammer。查哪几条见 `$PROD/docs/modules/M4/PORTTOOL-FLOW.md` C.4 |
| 全部 | `all` | 工装固件 + 四个平台的 PortTool + 交付包 |
| （给脚本用，不在菜单里） | `get KEY` | 打印 `tools/local.json` 的一个值，如 `TestCase/test.sh` 取 `HOST_CC` 给 `-race` 当 `CC` |

只编 PortTool 不需要这些：`go build ./src/cmd/porttool`。

工装固件在 `$BOOT` 源码的临时副本（`Output/build/fixture/`，编完即删）里编，镜像和 `VERSION` 留在 `Output/fixture/`，烧录和交付都从那里取；`$BOOT/Debug/` 和 IDE 的 workspace 不受影响（决策 86）。

## 面板上的字一律说大白话

**用户 2026-09-11 定：「所有提示信息都要用大白话来说明。」**

⚠️ **默认中文，另两种（English / Deutsch）同样说大白话**（决策 82）。面板上**人看的字一处不留成别的语言**：标题、按钮、参数名、下拉选项、读数标签、提示、错误。唯一的例外是**协议原文的回显**（日志窗里的 `pt.start dout ...`、帧原文）—— 那是给对机器的，照原样显示，但它旁边必须有说明。

**每一句都写进 `src/ptpanel/web/strings.json`，代码里只写键**；Go 回 `msg`（键加参数），不回句子。词典的形状和检查见 `$PROD/docs/modules/M4/PORTTOOL-FLOW.md` C.4.1。

（这一条只管面板。代码注释、`#error` 文案、工具的 stdout/stderr 仍然是英文，见 `~/.claude/rules/language.md`。）

`PortTool` 面板的受众是**硬件工程师**，不是写这套协议的人。所以面板上出现的每一句话：

| 不要 | 要 |
|---|---|
| 直接抄协议字面量（`loop=ctrl`、`mode=extloop`、`mv=1:500`） | 说清它是什么意思、要他做什么 |
| 协议里的英文标签（`duty`、`freq`、`hold`、`seq/rx/miss`） | 这一种语言里的说法（中文 `占空比`、`频率`、`保持`、`发/收/丢`） |
| 端口内部名（`dout3`、`rs4851`） | 工程师嘴里的名字（`DO3`、`RS485`），出处见 `Hardware/Klemmenbezeichnungen-R.pdf` |
| 裸错误码（`err=0x10000000`） | 译成原因，原始码可以跟在后面 |
| 只给数字 | 带单位、带范围、带"这算过还是不过" |

⚠️ **哪些数字不是判据，要在面板上说出来** —— 例如 `loop=ctrl` 端口回报的那三个计数只说明控制口活着，不是该端口的结论（`$PROD/docs/tables/DECISIONS.md` 第 9 条）。
