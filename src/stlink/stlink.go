// Package stlink erases and writes a board's flash over ST-Link, through
// STM32_Programmer_CLI: the bootloader and the factory reset after step 6.
// Calibration values are written by the fixture firmware instead (pt.calw,
// decision 142). What each operation does and why:
// $PROD/docs/modules/M4/PORTTOOL-FLOW.md, C.3.5. Addresses: SECTOR-15.md.
package stlink

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

const (
	FlashBase   = 0x08000000
	SectorSize  = 128 * 1024
	Sectors     = 16
	Sector15    = FlashBase + 15*SectorSize // 0x081E0000
	CalAreaSize = 8 * 1024
	// One flash word, the smallest unit H7 programs (SECTOR-15.md, layout).
	wordSize = 32
)

// CLI overrides the programmer's path. Tests set it; a bench never does.
var CLI string

// Run executes one programmer call and returns everything it printed. Tests
// replace it with stlinktest; nothing else does.
var Run = func(cli string, args ...string) (string, error) {
	out, err := exec.Command(cli, args...).CombinedOutput()
	return string(out), err
}

// TempDir is where the transfer files of one operation go. Tests set it.
var TempDir = os.TempDir

var errNotFound = errors.New("STM32_Programmer_CLI not found")

// ErrNotFound reports whether err means the programmer is not installed.
func ErrNotFound(err error) bool { return errors.Is(err, errNotFound) }

// Find locates STM32_Programmer_CLI the way the delivery's start.cmd does
// ($PORTTOOL/tools/delivery/start.cmd): the standalone install, PATH,
// then a search under the usual ST folders.
func Find() (string, error) {
	if CLI != "" {
		return CLI, nil
	}
	name := "STM32_Programmer_CLI"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	var fixed, roots []string
	if runtime.GOOS == "windows" {
		for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)"} {
			if d := os.Getenv(env); d != "" {
				fixed = append(fixed, filepath.Join(d, "STMicroelectronics", "STM32Cube", "STM32CubeProgrammer", "bin", name))
			}
		}
		fixed = append(fixed, filepath.Join(`C:\ST`, "STM32CubeProgrammer", "bin", name))
		if d := os.Getenv("ProgramFiles"); d != "" {
			roots = append(roots, filepath.Join(d, "ST"))
		}
		roots = append(roots, `C:\ST`, `D:\ST`)
	} else {
		// ST's Linux installer does not touch PATH. See PORTTOOL-FLOW.md C.3.5 in $PROD.
		sub := filepath.Join("STMicroelectronics", "STM32Cube", "STM32CubeProgrammer", "bin", name)
		if h, err := os.UserHomeDir(); err == nil {
			fixed = append(fixed, filepath.Join(h, sub))
		}
		fixed = append(fixed, filepath.Join("/usr/local", sub))
	}
	for _, p := range fixed {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	for _, r := range roots {
		found := ""
		_ = filepath.WalkDir(r, func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() && strings.EqualFold(d.Name(), name) {
				found = p
				return fs.SkipAll
			}
			return nil
		})
		if found != "" {
			return found, nil
		}
	}
	return "", errNotFound
}

// Programmer is one ST-Link attached to one board.
type Programmer struct{ cli string }

// Open finds the programmer.
func Open() (Programmer, error) {
	cli, err := Find()
	return Programmer{cli: cli}, err
}

func (p Programmer) call(args ...string) error {
	argv := append([]string{"-c", "port=SWD", "mode=UR"}, args...)
	out, err := Run(p.cli, argv...)
	// The CLI can exit 0 after printing an error, so its words are checked too.
	if err != nil || strings.Contains(out, "Error") {
		return fmt.Errorf("STM32_Programmer_CLI %s: %v\n%s", strings.Join(args, " "), err, lastLines(out, 6))
	}
	return nil
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func hex32(v int) string { return "0x" + strings.ToUpper(strconv.FormatInt(int64(v), 16)) }

func (p Programmer) tempFile(name string) (string, func(), error) {
	dir, err := os.MkdirTemp(TempDir(), "porttool-stlink-")
	if err != nil {
		return "", nil, err
	}
	return filepath.Join(dir, name), func() { os.RemoveAll(dir) }, nil
}

// Read uploads n bytes from addr.
func (p Programmer) Read(addr, n int) ([]byte, error) {
	path, done, err := p.tempFile("read.bin")
	if err != nil {
		return nil, err
	}
	defer done()
	if err := p.call("-u", hex32(addr), hex32(n), path); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) != n {
		return nil, fmt.Errorf("read %d bytes at %s, wanted %d", len(data), hex32(addr), n)
	}
	return data, nil
}

// Erase erases whole sectors by number.
func (p Programmer) Erase(sectors ...int) error {
	args := []string{"-e"}
	for _, s := range sectors {
		args = append(args, strconv.Itoa(s))
	}
	return p.call(args...)
}

// writeUsed writes only the flash words of data that are not blank, one call
// per contiguous run: blank words must stay erased so the bootloader can still
// append into them (PORTTOOL-FLOW.md C.3.5).
func (p Programmer) writeUsed(addr int, data []byte) error {
	blank := bytes.Repeat([]byte{0xFF}, wordSize)
	for start := 0; start < len(data); {
		if bytes.Equal(data[start:start+wordSize], blank) {
			start += wordSize
			continue
		}
		end := start
		for end < len(data) && !bytes.Equal(data[end:end+wordSize], blank) {
			end += wordSize
		}
		path, done, err := p.tempFile("write.bin")
		if err != nil {
			return err
		}
		err = os.WriteFile(path, data[start:end], 0o644)
		if err == nil {
			err = p.call("-w", path, hex32(addr+start), "-v")
		}
		done()
		if err != nil {
			return err
		}
		start = end
	}
	return nil
}

func (p Programmer) verify(addr int, want []byte) error {
	got, err := p.Read(addr, len(want))
	if err != nil {
		return err
	}
	if !bytes.Equal(got, want) {
		return fmt.Errorf("read back at %s differs from what was written", hex32(addr))
	}
	return nil
}

// FlashBootloader erases sectors 0-14 and writes the bootloader image (a .hex
// or .elf, which carry their own address). Sector 15 is left alone.
func (p Programmer) FlashBootloader(image string) error {
	sectors := make([]int, 0, 15)
	for s := 0; s <= 14; s++ {
		sectors = append(sectors, s)
	}
	if err := p.Erase(sectors...); err != nil {
		return err
	}
	return p.call("-w", image, "-v", "-rst")
}

// FactoryReset erases everything but the bootloader and the calibration
// values: the calibration area is read out, handed to keep (which must save it
// on the PC before anything is erased), sectors 1-15 are erased, and the area
// is written back (decision 94, STG-01 option A).
func (p Programmer) FactoryReset(keep func([]byte) error) error {
	cal, err := p.Read(Sector15, CalAreaSize)
	if err != nil {
		return err
	}
	if err := keep(cal); err != nil {
		return fmt.Errorf("calibration values not saved, nothing erased: %w", err)
	}
	sectors := make([]int, 0, 15)
	for s := 1; s <= 15; s++ {
		sectors = append(sectors, s)
	}
	if err := p.Erase(sectors...); err != nil {
		return err
	}
	if err := p.writeUsed(Sector15, cal); err != nil {
		return err
	}
	if err := p.verify(Sector15, cal); err != nil {
		return err
	}
	return p.call("-rst")
}
