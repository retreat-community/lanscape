package agent

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"time"

	"github.com/retreat-community/lanscape/internal/proto"
)

// AssetName is the release archive of an agent build, as goreleaser names it:
// lanscape-agent_1.2.0_linux_armv7.tar.gz, lanscape-agent_1.2.0_linux_mipsle_softfloat.tar.gz.
func AssetName(version, goos, goarch, arm, mips string) string {
	n := "lanscape-agent_" + version + "_" + goos + "_" + goarch
	if goarch == "arm" && arm != "" {
		n += "v" + arm
	}
	if strings.HasPrefix(goarch, "mips") && mips != "" {
		n += "_" + mips
	}
	if goos == "windows" {
		return n + ".zip"
	}
	return n + ".tar.gz"
}

// ownAsset names the archive of the running build.
func ownAsset(version string) string {
	var arm, mips string
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			switch s.Key {
			case "GOARM":
				arm = strings.TrimSuffix(strings.TrimSuffix(s.Value, ",softfloat"), ",hardfloat")
			case "GOMIPS", "GOMIPS64":
				mips = s.Value
			}
		}
	}
	if strings.HasPrefix(runtime.GOARCH, "mips") && mips == "" {
		mips = "softfloat" // the only MIPS flavour released
	}
	return AssetName(version, runtime.GOOS, runtime.GOARCH, arm, mips)
}

// ExtractBinary returns the agent executable from a release archive.
func ExtractBinary(archive []byte, isZip bool) ([]byte, error) {
	const limit = 256 << 20
	want := func(name string) bool {
		b := filepath.Base(name)
		return b == "lanscape-agent" || b == "lanscape-agent.exe"
	}
	if isZip {
		zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
		if err != nil {
			return nil, err
		}
		for _, f := range zr.File {
			if !want(f.Name) {
				continue
			}
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			defer rc.Close()
			return io.ReadAll(io.LimitReader(rc, limit))
		}
		return nil, errors.New("update: no lanscape-agent in the archive")
	}
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, errors.New("update: no lanscape-agent in the archive")
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag == tar.TypeReg && want(h.Name) {
			return io.ReadAll(io.LimitReader(tr, limit))
		}
	}
}

// Updatable reports why the binary cannot be replaced in place ("" when it can): images and
// packages managed elsewhere are updated by their own tools.
func Updatable(env Env) string {
	switch env.Kind {
	case "docker", "k8s-pod":
		return "the agent runs from a container image; update the image instead"
	}
	return ""
}

// SelfUpdate downloads the release named by m, verifies it and replaces the running binary.
// The caller restarts the process afterwards (the service manager starts the new binary).
func SelfUpdate(ctx context.Context, hc *http.Client, current string, m proto.UpdateMsg) (proto.UpdateResultMsg, error) {
	res := proto.UpdateResultMsg{From: current, To: m.Version}
	if why := Updatable(DetectEnv("", os.Getenv)); why != "" {
		return res, errors.New(why)
	}
	name := ownAsset(m.Version)
	sum := strings.ToLower(m.Checksums[name])
	if sum == "" {
		return res, fmt.Errorf("update: release %s has no %s", m.Version, name)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(m.BaseURL, "/")+"/"+name, nil)
	if err != nil {
		return res, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return res, fmt.Errorf("update: download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return res, fmt.Errorf("update: download %s: %s", name, resp.Status)
	}
	archive, err := io.ReadAll(io.LimitReader(resp.Body, 256<<20))
	if err != nil {
		return res, fmt.Errorf("update: download: %w", err)
	}
	if h := sha256.Sum256(archive); hex.EncodeToString(h[:]) != sum {
		return res, fmt.Errorf("update: %s: checksum mismatch", name)
	}
	bin, err := ExtractBinary(archive, strings.HasSuffix(name, ".zip"))
	if err != nil {
		return res, err
	}
	exe, err := os.Executable()
	if err != nil {
		return res, err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return res, err
	}
	if err := ReplaceBinary(ctx, exe, bin, m.Version); err != nil {
		return res, err
	}
	res.Detail = "updated to " + m.Version + ", restarting"
	return res, nil
}

// ReplaceBinary writes the new binary next to exe, checks that it runs and reports the
// expected version, then swaps it in; the old binary is kept as exe.old.
func ReplaceBinary(ctx context.Context, exe string, bin []byte, version string) error {
	tmp := exe + ".new"
	if err := os.WriteFile(tmp, bin, 0o755); err != nil { //nolint:gosec // an executable
		return fmt.Errorf("update: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, tmp, "version").Output() //nolint:gosec // our own new binary
	if err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("update: the new binary does not start on this host: %w", err)
	}
	if !strings.Contains(string(out), version) {
		_ = os.Remove(tmp)
		return fmt.Errorf("update: the new binary reports %q, not %s", strings.TrimSpace(string(out)), version)
	}
	old := exe + ".old"
	_ = os.Remove(old)
	// a running executable can be renamed (Windows) or replaced (Unix), not overwritten
	if err := os.Rename(exe, old); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("update: %w", err)
	}
	if err := os.Rename(tmp, exe); err != nil {
		_ = os.Rename(old, exe)
		return fmt.Errorf("update: %w", err)
	}
	return nil
}
