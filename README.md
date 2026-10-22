# OpenPLC_PortsTestingTool

`PortTool`: the port test panel for the OpenPLC board. A hardware engineer
opens it in a browser, drives each port of a board running the test firmware,
and gets a pass/fail report.

中文：[README.zh-CN.md](README.zh-CN.md)

## Building from source

Needs only Go 1.23 or newer. The first build downloads its modules, so it needs
network access. Run from the repository root:

```
go build -o PortTool ./src/cmd/porttool       # for this machine
```

On Windows name the output `PortTool.exe`. To build for another system, set
`GOOS` / `GOARCH` (no C compiler needed):

```
GOOS=linux   GOARCH=amd64 CGO_ENABLED=0 go build -o PortTool ./src/cmd/porttool
GOOS=darwin  GOARCH=arm64 go build -o PortTool ./src/cmd/porttool      # Apple silicon
GOOS=windows GOARCH=amd64 go build -o PortTool.exe ./src/cmd/porttool
```

The web page is compiled into the executable. The test plans are not: copy
`src/plans/*.json` into a `plans/` folder beside `PortTool`.

On Linux the user running PortTool needs access to the serial ports, usually
by being in the `dialout` group.

## Building everything and packing the delivery folder

`tools/build.sh` is the maintainers' entry point; it is not needed to build
PortTool. It runs `go run ./tools/ptbuild` from the repository root, so it
needs Go and, on Windows, Git Bash. Without arguments it shows a menu; with
arguments it runs that item directly. `start.sh` in the repository root
opens the same menu, so double-clicking it is enough. "All" empties `Output/`
first; a single build item deletes only what it is about to rebuild. Run `tools/build.sh find-tools` once
first: it finds the tools below and writes `tools/local.json`. Every item
checks the tools it needs before it starts and names any that are missing.

| Command | Does |
|---|---|
| `tools/build.sh find-tools` | finds this machine's tools, writes `tools/local.json` |
| `tools/build.sh tool [--os windows\|linux\|darwin\|darwin-arm64\|all]` | PortTool into `Output/bin/<platform>/`, with `plans/` beside it |
| `tools/build.sh fixture` | the test firmware into `Output/fixture/` |
| `tools/build.sh flash` | writes that firmware to the board over SWD |
| `tools/build.sh sim [--os ...]` | the simulated board beside PortTool, to use the panel without a board (port `sim`) |
| `tools/build.sh deliver` | the delivery folder `Output/delivery/` (Windows and Linux in one folder) |
| `tools/build.sh all` | firmware, PortTool for every platform, then the delivery folder |

Packing also needs, beside this repository:

| What | For |
|---|---|
| `open_plc_cube_ide` | the test firmware sources; its `Debug/open_plc_cube_ide.hex`, the bootloader built in STM32CubeIDE, goes into `stage6/` |
| STM32CubeIDE | builds the test firmware headless |
| `IAPTranfer_Tool`, with `Output/windows/IAPTool.exe` and `Output/linux/IAPTool` built | the upload tool in `stage6/` |
| `open_plc_arduino` | the FQBN the step-6 demo is compiled with |
| gcc or clang (MinGW on Windows) | the simulated board for this machine |
| zig | the simulated board for another system, cross-compiled |
| arduino-cli with the OpenPLC board package installed | compiles the step-6 demo `FixtureDemo` |

What goes into the folder: `$PROD/docs/modules/M4/PORTTOOL-FLOW.md` (in
`OpenPLC_Docs`), section C.4.
