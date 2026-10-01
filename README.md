# OpenPLC_PortsTestingTool

`PortTool`: the port test panel for the OpenPLC board. A hardware engineer
opens it in a browser, drives each port of a board running the test firmware,
and gets a pass/fail report.

中文：[README.zh-CN.md](README.zh-CN.md)

## Building from source

Needs only Go 1.23 or newer. The first build downloads its modules, so it needs
network access. Run from the repository root:

```
go build -o PortTool ./cmd/porttool       # for this machine
```

On Windows name the output `PortTool.exe`. To build for another system, set
`GOOS` / `GOARCH` (no C compiler needed):

```
GOOS=linux   GOARCH=amd64 CGO_ENABLED=0 go build -o PortTool ./cmd/porttool
GOOS=darwin  GOARCH=arm64 go build -o PortTool ./cmd/porttool      # Apple silicon
GOOS=windows GOARCH=amd64 go build -o PortTool.exe ./cmd/porttool
```

The web page is compiled into the executable. The test plans are not: copy
`TestCase/plans/*.json` into a `plans/` folder beside `PortTool`.

On Linux the user running PortTool needs access to the serial ports, usually
by being in the `dialout` group.

`compile_tool.sh`, `build.py` and `delivery.cmd` are the maintainers' scripts:
they also build the test firmware and pack the delivery folder, which needs the
firmware repository `open_plc_cube_ide` beside this one and STM32CubeIDE. Run
`python TestCase/tools/init_machine.py` once to record where they are. They are
not needed to build PortTool.
