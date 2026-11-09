//go:build darwin || linux

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
)

func TestInstalledCallerUnderDescriptorLimit(t *testing.T) {
	if os.Getenv("WAZA_ASSET_HELPER_LIMITED_CHILD") == "1" {
		var limit syscall.Rlimit
		if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &limit); err != nil {
			t.Fatal(err)
		}
		if limit.Max < 512 {
			t.Fatalf("descriptor-limit control requires hard limit >=512, got %d", limit.Max)
		}
		limit.Cur = 512
		if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &limit); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &limit); err != nil || limit.Cur != 512 {
			t.Fatalf("descriptor control did not set actual limit 512: limit=%d err=%v", limit.Cur, err)
		}
		var args []string
		if err := json.Unmarshal([]byte(os.Getenv("WAZA_ASSET_HELPER_TEST_ARGS")), &args); err != nil {
			t.Fatal(err)
		}
		os.Args = append([]string{"release-assets-helper"}, args...)
		main()
		os.Exit(0)
	}
	source := fixture(t)
	for i := range 195 {
		writeFixture(t, filepath.Join(source, "web/dist/assets", "part-"+strconv.Itoa(i)+".js"), []byte("part"))
	}
	artifact := filepath.Join(temp(t), "artifact")
	digest, err := caller(t, arguments("pack", source, artifact, "", testIdentity), hooks{})
	if err != nil {
		t.Fatal(err)
	}
	workspace := temp(t)
	for name, data := range installedFiles(t, source) {
		writeFixture(t, filepath.Join(workspace, "web/dist", name), []byte(data))
	}
	before := installedFiles(t, workspace)
	if len(before) != 200 {
		t.Fatalf("descriptor control has %d files, want 200", len(before))
	}
	for _, command := range []string{"install", "verify-installed"} {
		t.Run(command, func(t *testing.T) {
			encoded, err := json.Marshal(installArguments(command, artifact, workspace, digest))
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestInstalledCallerUnderDescriptorLimit$")
			cmd.Env = append(os.Environ(), "WAZA_ASSET_HELPER_LIMITED_CHILD=1", "WAZA_ASSET_HELPER_TEST_ARGS="+string(encoded))
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err = cmd.Run()
			if err != nil || stdout.String() != "manifest_sha256="+digest+"\n" || stderr.Len() != 0 {
				t.Fatalf("512-descriptor actual caller failed: %v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
			}
			if !equalFiles(before, installedFiles(t, workspace)) {
				t.Fatal("bounded caller changed the complete installed inventory")
			}
		})
	}
}
