package agent

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestAssetName(t *testing.T) {
	for _, c := range []struct{ goos, arch, arm, mips, want string }{
		{"linux", "amd64", "", "", "lanscape-agent_1.2.0_linux_amd64.tar.gz"},
		{"linux", "arm", "7", "", "lanscape-agent_1.2.0_linux_armv7.tar.gz"},
		{"linux", "mipsle", "", "softfloat", "lanscape-agent_1.2.0_linux_mipsle_softfloat.tar.gz"},
		{"windows", "amd64", "", "", "lanscape-agent_1.2.0_windows_amd64.zip"},
	} {
		if got := AssetName("1.2.0", c.goos, c.arch, c.arm, c.mips); got != c.want {
			t.Errorf("got %s want %s", got, c.want)
		}
	}
	if n := ownAsset("1.2.0"); n == "" {
		t.Error("no own asset")
	}
}

func TestExtractBinary(t *testing.T) {
	var tgz bytes.Buffer
	gz := gzip.NewWriter(&tgz)
	tw := tar.NewWriter(gz)
	for name, body := range map[string]string{"LICENSE": "gpl", "lanscape-agent": "ELF"} {
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg})
		_, _ = tw.Write([]byte(body))
	}
	_ = tw.Close()
	_ = gz.Close()
	if b, err := ExtractBinary(tgz.Bytes(), false); err != nil || string(b) != "ELF" {
		t.Errorf("tar: %q %v", b, err)
	}
	var z bytes.Buffer
	zw := zip.NewWriter(&z)
	w, _ := zw.Create("lanscape-agent.exe")
	_, _ = w.Write([]byte("MZ"))
	_ = zw.Close()
	if b, err := ExtractBinary(z.Bytes(), true); err != nil || string(b) != "MZ" {
		t.Errorf("zip: %q %v", b, err)
	}
	if _, err := ExtractBinary(z.Bytes(), false); err == nil {
		t.Error("zip read as tar")
	}
}

func TestReplaceBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script as the binary")
	}
	dir := t.TempDir()
	exe := filepath.Join(dir, "lanscape-agent")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\necho lanscape-agent 1.0.0\n"), 0o755); err != nil { //nolint:gosec // an executable
		t.Fatal(err)
	}
	broken := []byte("#!/bin/sh\nexit 1\n")
	if err := ReplaceBinary(context.Background(), exe, broken, "1.1.0"); err == nil {
		t.Fatal("a binary that does not start was installed")
	}
	good := []byte("#!/bin/sh\necho lanscape-agent 1.1.0\n")
	if err := ReplaceBinary(context.Background(), exe, good, "1.1.0"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe); !bytes.Equal(b, good) {
		t.Errorf("binary not replaced: %q", b)
	}
	if _, err := os.Stat(exe + ".old"); err != nil {
		t.Errorf("old binary not kept: %v", err)
	}
	if u := Updatable(Env{Kind: "docker"}); u == "" {
		t.Error("docker agent updatable")
	}
}
