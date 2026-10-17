@echo off
setlocal enabledelayedexpansion
cd /d "%~dp0"

rem 这个文件存的是 GBK，控制台就得用 936 读它 —— 不强制切过来的话，
rem 在一个 chcp 被改成 65001 的窗口里打开它，所有中文都是乱码。
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
  "%ProgramFiles%\STMicroelectronics\STM32Cube\STM32CubeProgrammerin\STM32_Programmer_CLI.exe"
  "%ProgramFiles(x86)%\STMicroelectronics\STM32Cube\STM32CubeProgrammerin\STM32_Programmer_CLI.exe"
  "C:\ST\STM32CubeProgrammerin\STM32_Programmer_CLI.exe"
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
