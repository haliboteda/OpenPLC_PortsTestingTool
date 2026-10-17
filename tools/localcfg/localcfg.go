// Package localcfg reads tools/local.json, this machine's tool paths, which
// `tools/build.sh find-tools` writes (decision 144). It is the only reader:
// tools/ptbuild and the host tests under TestCase/ all go through it.
package localcfg

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// FindRepo walks up from the working directory to the go.mod of module PortTool.
func FindRepo() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if b, err := os.ReadFile(filepath.Join(dir, "go.mod")); err == nil &&
			strings.HasPrefix(strings.TrimSpace(string(b)), "module PortTool") {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("not inside the PortTool repository (no go.mod of module PortTool above the working directory)")
		}
		dir = parent
	}
}

// Path is tools/local.json under the repository root.
func Path(repo string) string { return filepath.Join(repo, "tools", "local.json") }

// Load returns every key of tools/local.json; a missing or unreadable file reads as empty.
func Load(repo string) map[string]string {
	out := map[string]string{}
	if b, err := os.ReadFile(Path(repo)); err == nil {
		_ = json.Unmarshal(b, &out)
	}
	return out
}
