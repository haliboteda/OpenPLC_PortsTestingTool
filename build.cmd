@echo off
rem Double-click entry point. The menu and every build rule live in build.py -
rem this only starts it from the right directory and keeps the window open
rem afterwards, which a double-clicked script otherwise closes before anyone
rem has read the result.
rem
rem Arguments are passed through, so build.cmd --fixture --tool works from a
rem terminal too. The pause only happens when nobody gave any, because a
rem scripted call should not sit waiting for a key.

cd /d "%~dp0"
python build.py %*
set RC=%ERRORLEVEL%

if "%~1"=="" (
    echo.
    pause
)
exit /b %RC%
