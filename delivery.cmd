@echo off
rem One click: build the fixture firmware, build the PC tools, pack the folder
rem that goes to the hardware engineer. No menu - this script does one job, and
rem asking which part of it to do would be asking about steps nobody skips.
rem
rem The work is build.py's, not this file's; this only saves typing the flags
rem and keeps the window open so the result can be read.

cd /d "%~dp0"
python build.py --fixture --tool --deliver
set RC=%ERRORLEVEL%

if "%RC%"=="0" (
    echo.
    echo   把 Output\delivery 整个文件夹发给硬件工程师。
    echo   他那边只要装 STM32CubeProgrammer，然后双击里面的 start.cmd。
)

echo.
pause
exit /b %RC%
