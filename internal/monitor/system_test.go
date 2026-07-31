package monitor

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTemperatureFromThermalRootPrefersCPUSensor(t *testing.T) {
	root := t.TempDir()
	writeThermalZone(t, root, "thermal_zone0", "acpitz", "42000")
	writeThermalZone(t, root, "thermal_zone1", "x86_pkg_temp", "57500")

	value, ok := temperatureFromThermalRoot(root)
	if !ok || value != 57.5 {
		t.Fatalf("temperatureFromThermalRoot() = (%v, %t), want (57.5, true)", value, ok)
	}
}

func TestTemperatureFromThermalRootRejectsNonCPUSensor(t *testing.T) {
	root := t.TempDir()
	writeThermalZone(t, root, "thermal_zone0", "nvme", "48000")
	if value, ok := temperatureFromThermalRoot(root); ok || value != 0 {
		t.Fatalf("temperatureFromThermalRoot() = (%v, %t), want unavailable", value, ok)
	}
}

func writeThermalZone(t *testing.T, root, name, sensorType, temperature string) {
	t.Helper()
	zone := filepath.Join(root, name)
	if err := os.MkdirAll(zone, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(zone, "type"), []byte(sensorType), 0o644); err != nil {
		t.Fatalf("write type error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(zone, "temp"), []byte(temperature), 0o644); err != nil {
		t.Fatalf("write temp error = %v", err)
	}
}
