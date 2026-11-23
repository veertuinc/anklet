package github

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestRegisterRunnerBashSafeArgsNoShellInjectionAndNoTokenTrace(t *testing.T) {
	t.Parallel()

	runnerHome := t.TempDir()
	argsFile := filepath.Join(runnerHome, "config-args.txt")
	pwnFile := filepath.Join(runnerHome, "pwned")

	fakeConfigSh := `#!/usr/bin/env bash
set -euo pipefail
: > "` + argsFile + `"
for a in "$@"; do
  printf '%s\n' "$a" >> "` + argsFile + `"
done
`
	if err := os.WriteFile(filepath.Join(runnerHome, "config.sh"), []byte(fakeConfigSh), 0o755); err != nil {
		t.Fatal(err)
	}

	maliciousLabels := "anka-template:mac14,$(touch " + pwnFile + "),`touch " + pwnFile + "`;id"
	runnerGroup := "My Team Group"
	secretToken := "SUPER_SECRET_REG_TOKEN_999"

	cmd := exec.Command(
		"bash",
		"register-runner.bash",
		"anklet-vm-123",
		secretToken,
		"https://github.com/veertuinc",
		maliciousLabels,
		runnerGroup,
	)
	cmd.Env = append(os.Environ(), "RUNNER_HOME="+runnerHome)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("register-runner.bash failed: %v\noutput:\n%s", err, string(out))
	}

	// Shell injection must NOT create pwnFile.
	if _, statErr := os.Stat(pwnFile); statErr == nil {
		t.Fatal("command injection succeeded via labels in register-runner.bash")
	}
	// Token must NOT appear in stdout/stderr (no bash -x tracing).
	if strings.Contains(string(out), secretToken) {
		t.Fatalf("registration token leaked in script output: %s", string(out))
	}

	rawArgs, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	gotArgs := strings.Split(strings.TrimSuffix(string(rawArgs), "\n"), "\n")
	wantArgs := []string{
		"--name", "anklet-vm-123",
		"--token", secretToken,
		"--url", "https://github.com/veertuinc",
		"--no-default-labels",
		"--labels", maliciousLabels,
		"--disableupdate",
		"--unattended",
		"--ephemeral",
		"--runnergroup", runnerGroup,
	}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("config.sh args =\n%#v\nwant:\n%#v", gotArgs, wantArgs)
	}
}

func createTestRunnerTarGz(t *testing.T, destPath string) string {
	t.Helper()
	f, err := os.Create(destPath)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	content := []byte("#!/bin/sh\necho ok\n")
	if err := tw.WriteHeader(&tar.Header{
		Name: "run.sh",
		Mode: 0o755,
		Size: int64(len(content)),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestInstallRunnerBashVerifiesSHA256AndRejectsMismatch(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}

	arch := "arm64"
	if runtime.GOARCH == "amd64" {
		arch = "x64"
	}
	assetName := "actions-runner-osx-" + arch + "-2.321.0.tar.gz"
	archivePath := filepath.Join(t.TempDir(), assetName)
	validHash := createTestRunnerTarGz(t, archivePath)
	archiveBytes, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}

	runCase := func(t *testing.T, publishedHash string, wantErr bool) {
		t.Helper()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/releases/latest") {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{
					"assets": []map[string]any{
						{
							"name":                 assetName,
							"browser_download_url": "https://github.com/actions/runner/releases/download/v2.321.0/" + assetName,
							"digest":               "sha256:" + publishedHash,
						},
					},
				})
				return
			}
			_, _ = w.Write(archiveBytes)
		}))
		defer srv.Close()

		// Wrap curl on PATH so requests to api.github.com / github.com hit our local httptest.Server.
		fakeBin := t.TempDir()
		realCurl, err := exec.LookPath("curl")
		if err != nil {
			t.Fatal(err)
		}
		curlWrapper := `#!/usr/bin/env bash
args=()
for a in "$@"; do
  a="${a/https:\/\/api.github.com/` + srv.URL + `}"
  a="${a/https:\/\/github.com/` + srv.URL + `}"
  args+=("$a")
done
exec "` + realCurl + `" "${args[@]}"
`
		if err := os.WriteFile(filepath.Join(fakeBin, "curl"), []byte(curlWrapper), 0o755); err != nil {
			t.Fatal(err)
		}

		runnerHome := t.TempDir()
		cmd := exec.Command("bash", "install-runner.bash")
		cmd.Env = append(os.Environ(),
			"PATH="+fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"),
			"RUNNER_HOME="+runnerHome,
			"RUNNER_ARCH="+arch,
		)
		out, runErr := cmd.CombinedOutput()
		if wantErr {
			if runErr == nil {
				t.Fatalf("expected checksum mismatch failure, but install-runner.bash succeeded:\n%s", string(out))
			}
			if _, statErr := os.Stat(filepath.Join(runnerHome, "run.sh")); statErr == nil {
				t.Fatal("archive was extracted despite SHA-256 mismatch")
			}
			return
		}
		if runErr != nil {
			t.Fatalf("install-runner.bash failed unexpectedly: %v\n%s", runErr, string(out))
		}
		if _, statErr := os.Stat(filepath.Join(runnerHome, "run.sh")); statErr != nil {
			t.Fatalf("expected extracted run.sh in RUNNER_HOME: %v", statErr)
		}
	}

	t.Run("valid SHA-256 installs runner", func(t *testing.T) {
		runCase(t, validHash, false)
	})

	t.Run("mismatched SHA-256 aborts before extraction", func(t *testing.T) {
		runCase(t, strings.Repeat("0", 64), true)
	})
}
