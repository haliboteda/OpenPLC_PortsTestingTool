"""Packs what the hardware engineer needs into one folder.

    python tools/make_delivery.py            build the folder
    python tools/make_delivery.py --open     and open it in Explorer

The folder is the whole handover: a firmware image, something that flashes it,
the panel, and one page telling him what to do. Nothing in it needs this
repository, a compiler, or Python.

*** What he installs is one thing: STM32CubeProgrammer. *** Not CubeIDE - the
standalone programmer is a fraction of the size and is all that is needed to
put an image on a board. The flash script finds it in either place, because we
have it inside CubeIDE and he will not.

⚠️ It packs whatever is in Debug/ right now. Run `build.py --fixture` first,
or the engineer gets whichever image was built last - possibly a bootloader.

Exit 0 = packed, 1 = something was missing.
"""

import argparse
import re
import shutil
import subprocess
import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
import tool_repo  # noqa: E402,F401  - finds IAPTranfer_Tool's common.py

from common import EXE, Fail, Ok, Section, Warn, cfg  # noqa: E402

REPO = HERE.parent.parent
OUT = REPO / "Output" / "delivery"


def fixture_version():
    """The version the firmware reports over the wire, from its own source.

    Naming the image after it means the engineer can see which build he has
    without flashing it, and a folder sent last month is told apart from
    today's at a glance. Read rather than typed here: a version in two places
    is a version that will disagree.
    """
    src = Path(cfg.BOOT_REPO) / "TestCase" / "porttool" / "porttool.c"
    m = re.search(r'#define\s+PORTTOOL_VERSION\s+"([^"]+)"',
                  src.read_text(encoding="utf-8", errors="replace"))
    return m.group(1) if m else "unknown"

# %~dp0 is the folder this .cmd sits in, so the whole thing works from a USB
# stick or wherever it was copied to.
START_CMD = """@echo off
setlocal enabledelayedexpansion
cd /d "%~dp0"

rem \u8fd9\u4e2a\u6587\u4ef6\u5b58\u7684\u662f GBK\uff0c\u63a7\u5236\u53f0\u5c31\u5f97\u7528 936 \u8bfb\u5b83 \u2014\u2014 \u4e0d\u5f3a\u5236\u5207\u8fc7\u6765\u7684\u8bdd\uff0c
rem \u5728\u4e00\u4e2a chcp \u88ab\u6539\u6210 65001 \u7684\u7a97\u53e3\u91cc\u6253\u5f00\u5b83\uff0c\u6240\u6709\u4e2d\u6587\u90fd\u662f\u4e71\u7801\u3002
for /f "tokens=2 delims=:" %%C in ('chcp') do set "OLDCP=%%C"
chcp 936 >nul

:menu
cls
echo.
echo   ===============================================
echo     OpenPLC 工装 / OpenPLC fixture
echo   ===============================================
echo.
echo     1) 烧固件进板子      (先接好 ST-Link，板子上电)
echo        Flash the firmware  (ST-Link connected, board powered)
echo     2) 打开测试面板      (先接好 RS232 控制口)
echo        Open the test panel (RS232 control port connected)
echo     3) 两样都做          (先烧，再打开面板)
echo        Both                (flash, then open the panel)
echo     4) 看使用说明（中文）
echo     5) Read the guide (English)
echo     0) 退出 / Quit
echo.
set "PICK="
set /p "PICK=  选一个 / Choose [0-5]: "

rem 输入没了就退出。双击用的时候碰不到，可是被脚本调用时，
rem set /p 读不到东西也不报错 —— 不拦就是一个死循环。
if not defined PICK goto quit

if "%PICK%"=="1" goto flash
if "%PICK%"=="2" goto panel
if "%PICK%"=="3" goto both
if "%PICK%"=="4" goto help
if "%PICK%"=="5" goto help_en
if "%PICK%"=="0" goto quit
goto menu

:quit
if defined OLDCP chcp %OLDCP% >nul
exit /b 0

:both
call :do_flash
if errorlevel 1 goto after
call :do_panel
goto after

:flash
call :do_flash
goto after

:panel
call :do_panel
goto after

:help
start "" "%~dp0README.zh-CN.html"
goto menu

:help_en
start "" "%~dp0README.en.html"
goto menu

:after
echo.
pause
goto menu

rem ---------------------------------------------------------------- 烧固件
:do_flash
echo.
echo   --- 烧固件 / Flash the firmware ---
echo.

rem 烧录器装在哪都试一遍：独立版在前，装了整套 CubeIDE 的机器靠最后那一段搜。
set "CLI="
for %%P in (
  "%ProgramFiles%\STMicroelectronics\STM32Cube\STM32CubeProgrammer\bin\STM32_Programmer_CLI.exe"
  "%ProgramFiles(x86)%\STMicroelectronics\STM32Cube\STM32CubeProgrammer\bin\STM32_Programmer_CLI.exe"
  "C:\ST\STM32CubeProgrammer\bin\STM32_Programmer_CLI.exe"
) do if exist %%P set "CLI=%%~P"

if not defined CLI (
  for /f "delims=" %%F in ('where STM32_Programmer_CLI.exe 2^>nul') do set "CLI=%%F"
)

if not defined CLI (
  for %%R in ("%ProgramFiles%\ST" "C:\ST" "D:\ST") do (
    if exist %%R for /f "delims=" %%F in ('dir /b /s "%%~R\STM32_Programmer_CLI.exe" 2^>nul') do set "CLI=%%F"
  )
)

if not defined CLI (
  echo   [x] 没找到 STM32CubeProgrammer。 / STM32CubeProgrammer not found.
  echo.
  echo       去 ST 官网免费下载安装，装完再回来：
  echo       Download it free from ST, install it, then come back:
  echo       https://www.st.com/en/development-tools/stm32cubeprog.html
  exit /b 2
)

rem 用通配符找镜像：批处理的代码里不出现中文文件名，
rem 否则在不同代码页的机器上会找不到文件。
set "IMG="
set /a NHEX=0
for %%H in ("%~dp0*.hex") do (
  set "IMG=%%~fH"
  set /a NHEX+=1
)
if !NHEX! EQU 0 (
  echo   [x] 这个文件夹里没有 .hex 固件文件。 / No .hex firmware file in this folder.
  exit /b 2
)
if !NHEX! GTR 1 (
  echo   [x] 这个文件夹里有 !NHEX! 个 .hex，不知道该烧哪个。
  echo       There are !NHEX! .hex files here - which one to flash is unclear.
  echo       只留下要烧的那一个，把其他的移走。 / Keep only the one to flash.
  exit /b 2
)

for %%N in ("!IMG!") do echo   固件 / Firmware: %%~nxN
echo.

"!CLI!" -c port=SWD mode=UR -d "!IMG!" -v -rst
if errorlevel 1 (
  echo.
  echo   [x] 没烧进去。检查：ST-Link 插了吗？板子上电了吗？SWD 线接牢了吗？
  echo   [x] Flashing failed. Check: ST-Link plugged in? Board powered? SWD wires firm?
  exit /b 1
)
echo.
echo   [ok] 烧好了，板子已经重启。 / Flashed; the board has restarted.
exit /b 0

rem ---------------------------------------------------------------- 开面板
:do_panel
echo.
echo   --- 打开测试面板 / Open the test panel ---
echo.
echo   浏览器会自己打开。那个新窗口别关，关了面板就停。
echo   A browser opens by itself. Keep the new window open: closing it stops the panel.
echo.
start "OpenPLC PortTool" "%~dp0PortTool.exe"
exit /b 0
"""

README_ZH = """# OpenPLC 工装 —— 怎么用

English: `README.en.html`

这个文件夹就是全部，不需要装开发环境。

## 一、先装一个软件

**STM32CubeProgrammer** —— ST 官网免费下载，装上就行。

<https://www.st.com/en/development-tools/stm32cubeprog.html>

⚠️ **不是** STM32CubeIDE。要的是那个几十兆的烧录器，不是一整套开发环境。ST-Link 的驱动跟着它一起装好。

## 二、双击 `start.cmd`

只有这一个入口，双击它会出菜单：

```
    1) 烧固件进板子      (先接好 ST-Link，板子上电)
    2) 打开测试面板      (先接好 RS232 控制口)
    3) 两样都做          (先烧，再打开面板)
    4) 看使用说明（中文）
    5) Read the guide (English)
    0) 退出 / Quit
```

输数字回车就行。做完回到菜单，可以接着做下一件。

## 三、测端口

菜单选 2（或 3），浏览器会自己打开一个页面。

1. 左边选板子的 **RS232 控制口**（一般是插在电脑上那个 USB 转串口，端子 C05/C06）
2. 点「连接」
3. 左边出现所有端口（以太网、USB、SD 卡、SDRAM、数字输入输出、继电器、模拟量、CAN、KNX、RS485……）
4. 点一个端口 → 看卡片上写的「测什么 / 怎么做 / 什么算过」→ 点「开始」

**每张卡都写清楚了这一项要接什么线、什么数算过。** 不用记命令。

⚠️ 面板打开后会多一个黑窗口，**别关它**，关了面板就停。测完再关。

⚠️ 旁边那个 `plans` 文件夹别删 —— **每一项「什么算过」都写在里面**，删了面板就只能给读数、给不出结论。

## 四、有些项目板子自己测不了

板上没有采样通路的那几项，**只能用万用表量**：待机功耗、3.3V / 5V / 5V_EXT、高边输出的电压电流、继电器触点的电压电流、模拟输出电流。面板在对应的卡片上会写明「这个数板子读不回来」。

模拟输出还有一张**多点测量**卡：它逐点输出，每一点停下来等你把万用表读数填进去，最后算出增益和零点偏差。
"""

# The same guide in English. Change both together (DECISIONS.md 75 in $PROD).
README_EN = """# OpenPLC fixture - how to use it

中文：`README.zh-CN.html`

This folder is everything. No development tools are needed.

## 1. Install one program

**STM32CubeProgrammer** - a free download from ST.

<https://www.st.com/en/development-tools/stm32cubeprog.html>

⚠️ **Not** STM32CubeIDE. What is needed is the small programmer, not the whole development environment. The ST-Link driver is installed with it.

## 2. Double-click `start.cmd`

It is the only entry point. It shows a menu:

```
    1) Flash the firmware  (ST-Link connected, board powered)
    2) Open the test panel (RS232 control port connected)
    3) Both                (flash, then open the panel)
    4) 看使用说明（中文）
    5) Read the guide (English)
    0) Quit
```

Type a number and press Enter. After each job it returns to the menu.

## 3. Test the ports

Choose 2 (or 3) and a browser page opens by itself.

1. On the left, pick the board's **RS232 control port** (usually the USB-serial adapter plugged into this PC, terminals C05/C06)
2. Click Connect
3. Every port appears on the left (Ethernet, USB, SD card, SDRAM, digital in/out, relays, analog, CAN, KNX, RS485 ...)
4. Click a port, read what its card says - what is tested, how, and what counts as a pass - then click Start

**Every card says which wires this test needs and which values pass.** No commands to remember.

⚠️ Opening the panel also opens a black window. **Do not close it** while testing: closing it stops the panel.

⚠️ Do not delete the `plans` folder next to it - **what counts as a pass for every test is written there**. Without it the panel shows readings but cannot give a verdict.

## 4. What the board cannot test by itself

A few items have no measuring path on the board and **need a multimeter**: standby power, 3.3 V / 5 V / 5V_EXT, high-side output voltage and current, relay contact voltage and current, analog output current. The panel says so on those cards ("the board cannot read this value back").

Analog output also has a **multi-point** card: it steps through set points, waits at each one for you to type in the multimeter reading, then works out gain and offset.
"""


def main():
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--open", action="store_true", help="做完在资源管理器里打开")
    args = ap.parse_args()

    hexfile = Path(cfg.BOOT_REPO) / "Debug" / "open_plc_cube_ide.hex"
    panel = REPO / "Output" / "windows" / ("PortTool" + EXE)
    plans = REPO / "TestCase" / "plans"

    Section("检查要打包的东西")
    missing = [str(p) for p in (hexfile, panel, plans) if not p.exists()]
    if missing:
        Fail("少了：" + "，".join(missing))
        print("  先跑：python build.py --fixture   然后   python build.py --tool")
        return 1
    print("  固件  {}  ({:,} 字节)".format(hexfile.name, hexfile.stat().st_size))
    print("  面板  {}  ({:,} 字节)".format(panel.name, panel.stat().st_size))
    print("  方案  {} 个".format(len(list(plans.glob("*.json")))))

    Section("打包")
    # 删里面的东西，不删文件夹本身 —— 资源管理器开着它的时候
    # rmtree 会失败，而“刚手动看过一眼”正是重新打包前最常发生的事。
    OUT.mkdir(parents=True, exist_ok=True)
    for old_file in OUT.iterdir():
        try:
            if old_file.is_dir():
                shutil.rmtree(old_file)
            else:
                old_file.unlink()
        except PermissionError:
            Fail("删不掉 " + old_file.name + " —— 它正开着？先关掉再跑。")
            return 1

    shutil.copy2(hexfile, OUT / ("openplc_fixture_%s.hex" % fixture_version()))
    shutil.copy2(panel, OUT / "PortTool.exe")

    # *** 方案文件不是可选的。*** 判据就写在里面 —— 没有它，面板连上板子也只能
    # 报“没有判据”，每个端口都判不了。面板先找 exe 旁边的 plans/，所以放这儿。
    dst_plans = OUT / "plans"
    dst_plans.mkdir()
    for f in sorted(plans.glob("*.json")):
        shutil.copy2(f, dst_plans / f.name)
    # ⚠️ GBK，不是 UTF-8。cmd.exe 按控制台代码页读批处理，而中文 Windows 那个
    # 代码页是 936；UTF-8 的中文在那里是乱码，连带着那一行命令一起坏掉。
    # ⚠️ GBK，不是 UTF-8。cmd.exe 按控制台代码页读批处理，而中文 Windows
    # 那个代码页是 936；UTF-8 的中文在那里是乱码，连带着那一行命令一起坏掉。
    crlf = START_CMD.replace(chr(10), chr(13) + chr(10))
    (OUT / "start.cmd").write_bytes(crlf.encode("gbk"))

    for name, text in (("README.zh-CN", README_ZH), ("README.en", README_EN)):
        md = OUT / (name + ".md")
        md.write_text(text, encoding="utf-8")
        rc = subprocess.call([sys.executable, str(HERE / "md2html.py"),
                              str(md), str(OUT / (name + ".html"))])
        if rc == 0:
            md.unlink()          # the engineer reads the HTML, not the source
        else:
            Warn(name + " 没转成 HTML，markdown 原文留在文件夹里了。")

    for f in sorted(OUT.iterdir()):
        if f.is_dir():
            n = len(list(f.iterdir()))
            print("  {:<32} {} 个文件".format(f.name + "\\", n))
        else:
            print("  {:<32} {:>10,} 字节".format(f.name, f.stat().st_size))

    Section("结果")
    Ok("交付文件夹做好了：" + str(OUT))
    print("  整个文件夹发给硬件工程师。他那边只要装 STM32CubeProgrammer。")
    if args.open:
        subprocess.call(["explorer", str(OUT)])
    return 0


if __name__ == "__main__":
    sys.exit(main())
