# OpenPLC fixture - how to use it

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

1. Top left, "Connect": pick the board's **RS232 control port** (usually the USB-serial adapter plugged into this PC, terminals C05/C06) and click Connect
2. On the left, pick a test type: **R&D test** runs items one by one, by hand; in the **full fixture test** you tick functional, aging, safety and user-level, set them up and run them in one go
3. In the R&D test, pick a port in the middle; its item cards appear on the right, each saying what is tested, what to connect and what counts as a pass
4. Tick the cards to run and click Start, top right. Anything a person has to measure or look at comes up as a question marked 🍍; fill it in

**No commands to remember.** For items with a peer (USB, network, RS485, CAN, reference board), connect that peer under "Connect" first.

⚠️ Opening the panel also opens a black window. **Do not close it** while testing: closing it stops the panel.

⚠️ Do not delete the `plans` folder next to it - **what counts as a pass for every test is written there**. Without it the panel shows readings but cannot give a verdict.

⚠️ Do not delete the `stage6` folder either - step 6 (the user-level test) uses the bootloader, IAPTool and demo in it.

## 4. Where a multimeter is needed

Values the board cannot read back itself, such as analog output current or a loaded digital output, make the panel stop and ask what the multimeter reads; it judges what you type. Only in the R&D test; the full fixture test reads them on the reference board and asks nobody.

Analog calibration is in the **R&D test** only: type the multimeter reading at each point, the panel fits each channel's k and b, keeps them on this PC, and burns them into the board with the ST-Link.

## 5. On Linux

The Linux files are in this same folder. Everything above applies, except for the points below.

1. **Entry point**: in a terminal, go to this folder and run `bash start.sh`. It shows the same menu as `start.cmd`. Do not double-click it: that usually just opens it as text.
2. **Install the Linux STM32CubeProgrammer**: same download page. Install it in the default place (`~/STMicroelectronics/STM32Cube/STM32CubeProgrammer` or `/usr/local/STMicroelectronics/STM32Cube/STM32CubeProgrammer`), or put its `bin` on PATH, and the menu and the panel find it.
3. **Serial port access**: your user must be in the `dialout` group, or the serial ports will not open. Run `sudo usermod -aG dialout $USER`, then log out and back in.
4. **KNX USB interface**: on Linux it goes through `/dev/hidraw*`, which only root can open by default. Add a udev rule to open it up, for example `SUBSYSTEM=="hidraw", ATTRS{idVendor}=="<vendor id>", MODE="0666"` in `/etc/udev/rules.d/99-knx-usb.rules` (find the vendor id with `lsusb`), then unplug and replug the interface.
5. **The panel runs in the terminal**: after choosing 2 (or 3), the panel runs in the terminal `start.sh` is in. Ctrl+C stops it and returns to the menu. Only press it when testing is done.
