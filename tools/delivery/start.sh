#!/usr/bin/env bash
cd "$(dirname "$0")" || exit 1

# A zip or a USB stick loses the executable bit.
chmod +x PortTool stage6/IAPTool porttool_simboard 2>/dev/null

# Same places as $PORTTOOL/src/stlink/stlink.go Find() on Linux, same order.
find_cli() {
  local sub=STMicroelectronics/STM32Cube/STM32CubeProgrammer/bin/STM32_Programmer_CLI
  local p
  for p in "$HOME/$sub" "/usr/local/$sub"; do
    if [ -f "$p" ]; then
      echo "$p"
      return 0
    fi
  done
  command -v STM32_Programmer_CLI
}

open_doc() {
  if command -v xdg-open >/dev/null 2>&1; then
    xdg-open "$1" >/dev/null 2>&1 &
  else
    echo "  用浏览器打开 / Open in a browser: $(pwd)/$1"
    pause
  fi
}

pause() {
  echo
  read -r -p "  按回车继续 / Press Enter to continue " _ || exit 0
}

do_flash() {
  echo
  echo "  --- 烧固件 / Flash the firmware ---"
  echo
  local cli
  cli="$(find_cli)"
  if [ -z "$cli" ]; then
    echo "  [x] 没找到 STM32CubeProgrammer。 / STM32CubeProgrammer not found."
    echo
    echo "      去 ST 官网免费下载 Linux 版安装，装完再回来："
    echo "      Download the Linux version free from ST, install it, then come back:"
    echo "      https://www.st.com/en/development-tools/stm32cubeprog.html"
    return 2
  fi

  local imgs
  shopt -s nullglob
  imgs=(./*.hex)
  shopt -u nullglob
  if [ "${#imgs[@]}" -eq 0 ]; then
    echo "  [x] 这个文件夹里没有 .hex 固件文件。 / No .hex firmware file in this folder."
    return 2
  fi
  if [ "${#imgs[@]}" -gt 1 ]; then
    echo "  [x] 这个文件夹里有 ${#imgs[@]} 个 .hex，不知道该烧哪个。"
    echo "      There are ${#imgs[@]} .hex files here - which one to flash is unclear."
    echo "      只留下要烧的那一个，把其他的移走。 / Keep only the one to flash."
    return 2
  fi

  echo "  固件 / Firmware: $(basename "${imgs[0]}")"
  echo

  if ! "$cli" -c port=SWD mode=UR -d "${imgs[0]}" -v -rst; then
    echo
    echo "  [x] 没烧进去。检查：ST-Link 插了吗？板子上电了吗？SWD 线接牢了吗？"
    echo "  [x] Flashing failed. Check: ST-Link plugged in? Board powered? SWD wires firm?"
    return 1
  fi
  echo
  echo "  [ok] 烧好了，板子已经重启。 / Flashed; the board has restarted."
  return 0
}

# The panel runs in this terminal; Ctrl+C stops it and returns to the menu.
do_panel() {
  echo
  echo "  --- 打开测试面板 / Open the test panel ---"
  echo
  echo "  浏览器会自己打开。面板在这个终端里运行，按 Ctrl+C 停掉面板、回到菜单。"
  echo "  A browser opens by itself. The panel runs in this terminal: Ctrl+C stops it and returns to the menu."
  echo
  # A handler, not '', so PortTool still gets Ctrl+C and the menu survives it.
  trap ':' INT
  ./PortTool
  trap - INT
  return 0
}

while true; do
  clear 2>/dev/null
  echo
  echo "  ==============================================="
  echo "    OpenPLC 工装 / OpenPLC fixture"
  echo "  ==============================================="
  echo
  echo "    1) 烧固件进板子      (先接好 ST-Link，板子上电)"
  echo "       Flash the firmware  (ST-Link connected, board powered)"
  echo "    2) 打开测试面板      (先接好 RS232 控制口)"
  echo "       Open the test panel (RS232 control port connected)"
  echo "    3) 两样都做          (先烧，再打开面板)"
  echo "       Both                (flash, then open the panel)"
  echo "    4) 看使用说明（中文）"
  echo "    5) Read the guide (English)"
  echo "    0) 退出 / Quit"
  echo
  PICK=""
  # No input left (called from a script) quits instead of looping forever.
  read -r -p "  选一个 / Choose [0-5]: " PICK || exit 0
  case "$PICK" in
    1) do_flash; pause ;;
    2) do_panel; pause ;;
    3) do_flash && do_panel; pause ;;
    4) open_doc README.zh-CN.html ;;
    5) open_doc README.en.html ;;
    0|"") exit 0 ;;
  esac
done
