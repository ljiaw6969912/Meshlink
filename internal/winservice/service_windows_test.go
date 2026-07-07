//go:build windows

package winservice

import "testing"

func TestServiceBinaryPathCarriesUpdatedConfigPath(t *testing.T) {
	got := serviceBinaryPath(
		`C:\Program Files\Meshlink\mesh-agent.exe`,
		"MeshlinkAgent",
		`C:\ProgramData\Meshlink\configs\active config.json`,
	)
	want := `"C:\Program Files\Meshlink\mesh-agent.exe" -service run -service-name MeshlinkAgent -config "C:\ProgramData\Meshlink\configs\active config.json"`
	if got != want {
		t.Fatalf("serviceBinaryPath() = %q, want %q", got, want)
	}
}

func TestConfigPathFromServiceBinaryPath(t *testing.T) {
	binaryPath := serviceBinaryPath(
		`C:\Program Files\Meshlink\mesh-agent.exe`,
		"MeshlinkAgent",
		`C:\ProgramData\Meshlink\configs\active config.json`,
	)
	got := configPathFromBinaryPath(binaryPath)
	want := `C:\ProgramData\Meshlink\configs\active config.json`
	if got != want {
		t.Fatalf("configPathFromBinaryPath() = %q, want %q", got, want)
	}
}
