package update

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// These tests run the real installer with only SCM/process operations replaced.
// File extraction, manifest validation, copying, rollback and results are real.
func TestApplyInstallerIsolated(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("PowerShell installer")
	}
	for _, scenario := range []string{"success", "copy-failure", "restart-failure", "wait-failure", "process-failure", "hash-failure", "runtime-data", "zip-traversal", "target-link"} {
		t.Run(scenario, func(t *testing.T) {
			base := t.TempDir()
			files := map[string]string{"VERSION": "0.2.0\n", "bin/wintun.dll": "new driver", "scripts/start.ps1": "new script", "使用说明.txt": "new instructions"}
			for _, name := range requiredUpdateFiles[1:] {
				files[name] = "new executable"
			}
			if scenario == "runtime-data" {
				files["configs/private.json"] = "must not install"
			}
			if scenario == "zip-traversal" {
				files["../outside.txt"] = "must not extract"
			}
			for _, name := range []string{"VERSION", "bin/mesh-desktop.exe", "scripts/start.ps1", "bin/wintun.dll", "configs/private.json", "certs/key.pem", "profiles/state.json"} {
				p := filepath.Join(base, filepath.FromSlash(name))
				if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
					t.Fatal(err)
				}
				value := "original"
				if name == "VERSION" {
					value = "0.1.0\n"
				}
				if err := os.WriteFile(p, []byte(value), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "target-link" {
				outside := filepath.Join(t.TempDir(), "outside-script.ps1")
				if err := os.WriteFile(outside, []byte("original"), 0600); err != nil {
					t.Fatal(err)
				}
				link := filepath.Join(base, "scripts", "start.ps1")
				if err := os.Remove(link); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, link); err != nil {
					t.Skipf("symlink privilege unavailable: %v", err)
				}
			}
			pkg := filepath.Join(t.TempDir(), "meshlink-0.2.0.zip")
			writeApplyTestPackage(t, pkg, files)
			opts := ApplyOptions{BaseDir: base, PackagePath: pkg, Version: "0.2.0"}
			if scenario == "wait-failure" {
				opts.WaitPID = 250
			}
			if scenario == "hash-failure" {
				opts.PackageSHA256 = strings.Repeat("0", 64)
			}
			scriptPath, err := WriteApplyScript(opts)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(scriptPath)
			if err != nil {
				t.Fatal(err)
			}
			prefix := "$Scenario = " + psQuote(scenario) + "\n$TestBase = " + psQuote(base) + "\n" + fakeRuntimePS
			if err := os.WriteFile(scriptPath, append([]byte("\xef\xbb\xbf"+prefix), raw[3:]...), 0600); err != nil {
				t.Fatal(err)
			}
			out, runErr := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", scriptPath).CombinedOutput()
			if scenario == "success" && runErr != nil {
				t.Fatalf("installer: %v\n%s", runErr, out)
			}
			if scenario != "success" && runErr == nil {
				t.Fatalf("installer unexpectedly succeeded\n%s", out)
			}
			result, err := ReadApplyResult(base)
			if err != nil {
				t.Fatalf("result: %v\n%s", err, out)
			}
			wantStatus := "failed"
			if scenario == "success" {
				wantStatus = "success"
			}
			if result.Status != wantStatus {
				t.Fatalf("result=%+v\n%s", result, out)
			}
			for _, name := range []string{"configs/private.json", "certs/key.pem", "profiles/state.json"} {
				b, _ := os.ReadFile(filepath.Join(base, filepath.FromSlash(name)))
				if string(b) != "original" {
					t.Fatalf("runtime data changed: %s", name)
				}
			}
			for _, name := range []string{"bin/mesh-desktop.exe", "scripts/start.ps1", "bin/wintun.dll"} {
				b, _ := os.ReadFile(filepath.Join(base, filepath.FromSlash(name)))
				want := "original"
				if scenario == "success" {
					want = files[name]
				}
				if string(b) != want {
					t.Fatalf("%s = %q, want %q\n%s", name, b, want, out)
				}
			}
			if scenario == "success" || scenario == "copy-failure" || scenario == "restart-failure" || scenario == "process-failure" {
				state, _ := os.ReadFile(filepath.Join(base, "fake-service-state"))
				if string(state) != "Automatic:Running" {
					t.Fatalf("service state %q\n%s", state, out)
				}
			}
			if scenario == "wait-failure" {
				if _, err := os.Stat(filepath.Join(base, "fake-service-state")); !os.IsNotExist(err) {
					t.Fatal("wait failure must leave services untouched")
				}
			}
			launch, _ := os.ReadFile(filepath.Join(base, "fake-desktop-args"))
			if string(launch) != "-update-result" {
				t.Fatalf("desktop args %q\n%s", launch, out)
			}
		})
	}
}

func writeApplyTestPackage(t *testing.T, filename string, files map[string]string) {
	t.Helper()
	m := packageManifest{Schema: PackageManifestSchema, Product: "Meshlink", Version: "0.2.0", Mode: DevelopmentMode}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		sum := sha256.Sum256([]byte(files[name]))
		m.Files = append(m.Files, packageFileInfo{Path: name, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(files[name]))})
	}
	manifest, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filename)
	if err != nil {
		t.Fatal(err)
	}
	z := zip.NewWriter(f)
	for _, name := range names {
		w, err := z.Create("meshlink/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = w.Write([]byte(files[name])); err != nil {
			t.Fatal(err)
		}
	}
	w, err := z.Create("meshlink/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.Write(manifest); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

const fakeRuntimePS = `
Import-Module (Join-Path $PSHOME 'Modules\Microsoft.PowerShell.Utility\Microsoft.PowerShell.Utility.psd1') -Force
$script:FakeStatus = 'Running'
$script:FakeMode = 'Automatic'
$script:FakeProcess = $true
$script:FailedOnce = $false
function Save-FakeState { [IO.File]::WriteAllText((Join-Path $TestBase 'fake-service-state'), ($script:FakeMode + ':' + $script:FakeStatus)) }
function Get-CimInstance($ClassName) {
  if ($ClassName -eq 'Win32_Service') {
    [pscustomobject]@{Name='FakeMeshlink'; PathName=('"' + $TestBase + '\bin\mesh-update-server.exe" serve'); StartMode='Auto'; State='Running'}
    [pscustomobject]@{Name='OtherInstall'; PathName='"C:\other-install\bin\mesh-agent.exe"'; StartMode='Auto'; State='Running'}
  }
  if ($ClassName -eq 'Win32_Process') {
    if ($script:FakeProcess) { [pscustomobject]@{ExecutablePath=($TestBase + '\bin\mesh-desktop.exe'); ProcessId=250} }
    [pscustomobject]@{ExecutablePath='C:\other-install\bin\mesh-desktop.exe'; ProcessId=251}
  }
}
function Stop-Process($Id,[switch]$Force,$ErrorAction) {
  if ($Scenario -eq 'wait-failure') { throw 'WaitPID was killed before waiting' }
  if ($Id -ne 250) { throw 'touched unrelated process' }
  if ($Scenario -eq 'process-failure') { throw 'cannot stop process' }
  $script:FakeProcess=$false
}
function Set-Service($Name, $StartupType) { if ($Name -ne 'FakeMeshlink') { throw 'touched unrelated service' }; $script:FakeMode=$StartupType; Save-FakeState }
function Get-Service($Name) {
  if ($Name -ne 'FakeMeshlink') { throw 'touched unrelated service' }
  $Svc=[pscustomobject]@{Status=$script:FakeStatus}
  $Svc | Add-Member -MemberType ScriptMethod -Name WaitForStatus -Value { param($Status,$Timeout); if ($script:FakeStatus -ne $Status) { throw 'fake service wait failed' } }
  return $Svc
}
function Stop-Service($Name, [switch]$Force) { if ($Name -ne 'FakeMeshlink') { throw 'touched unrelated service' }; $script:FakeStatus='Stopped'; Save-FakeState }
function Start-Service($Name) {
  if ($Name -ne 'FakeMeshlink') { throw 'touched unrelated service' }
  if ($Scenario -eq 'restart-failure' -and -not $script:FailedOnce) { $script:FailedOnce=$true; throw 'simulated restart failure' }
  $script:FakeStatus='Running'; Save-FakeState
}
function Start-Process($FilePath,$ArgumentList,$WorkingDirectory) { [IO.File]::WriteAllText((Join-Path $TestBase 'fake-desktop-args'),[string]$ArgumentList) }
function Get-Process($Id,$ErrorAction) { if ($Scenario -eq 'wait-failure') { [pscustomobject]@{Id=$Id} } }
function Wait-Process($Id,$Timeout,$ErrorAction) { throw 'requesting process did not exit' }
function Copy-Item($LiteralPath,$Destination,[switch]$Force) {
  if ($script:FakeStatus -ne 'Stopped') { throw 'copy while service running' }
  if ($script:FakeProcess) { throw 'copy while process running' }
  if ($Scenario -eq 'copy-failure' -and -not $script:FailedOnce -and $LiteralPath -like '*\stage\*' -and $Destination -like '*mesh-desktop.exe') { $script:FailedOnce=$true; throw 'simulated copy failure' }
  Microsoft.PowerShell.Management\Copy-Item -LiteralPath $LiteralPath -Destination $Destination -Force
}
`
