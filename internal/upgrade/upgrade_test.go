package upgrade

import (
	"os"
	"path/filepath"
	"testing"
)

func readDocFile(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join("..", "..", "docs", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("не удалось прочитать %s: %v", path, err)
	}
	return string(data)
}

func TestNormalizeVersion(t *testing.T) {
	cases := map[string]string{
		"9.14.1P17": "9.14.1",
		"9.8P1":     "9.8",
		"9.13.1":    "9.13.1",
		"9.0":       "9.0",
	}

	for raw, want := range cases {
		if got := NormalizeVersion(raw); got != want {
			t.Errorf("NormalizeVersion(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		v1, v2 string
		want   int
	}{
		{"9.8", "9.8.0", 0},
		{"9.8", "9.12.1", -1},
		{"9.12.1", "9.8", 1},
		{"9.15.1", "9.15.1", 0},
		{"9.14.1P17", "9.14.1", 0},
	}

	for _, c := range cases {
		if got := CompareVersions(c.v1, c.v2); got != c.want {
			t.Errorf("CompareVersions(%q, %q) = %d, want %d", c.v1, c.v2, got, c.want)
		}
	}
}

func TestFindUpgradePath_9_8_to_9_14_1(t *testing.T) {
	content := readDocFile(t, "Upgrade-path.txt")
	edges, err := ParseUpgradePath(content)
	if err != nil {
		t.Fatalf("ParseUpgradePath: %v", err)
	}

	path, err := FindUpgradePath(edges, "9.8", "9.14.1")
	if err != nil {
		t.Fatalf("FindUpgradePath: %v", err)
	}

	want := []string{"9.8", "9.12.1", "9.13.1", "9.14.1"}
	if len(path) != len(want) {
		t.Fatalf("FindUpgradePath(9.8 -> 9.14.1) = %v, want %v", path, want)
	}
	for i := range want {
		if path[i] != want[i] {
			t.Fatalf("FindUpgradePath(9.8 -> 9.14.1) = %v, want %v", path, want)
		}
	}
}

func TestFindUpgradePath_AnyHigher(t *testing.T) {
	content := readDocFile(t, "Upgrade-path.txt")
	edges, err := ParseUpgradePath(content)
	if err != nil {
		t.Fatalf("ParseUpgradePath: %v", err)
	}

	path, err := FindUpgradePath(edges, "9.15.1", "9.18.1")
	if err != nil {
		t.Fatalf("FindUpgradePath(9.15.1 -> 9.18.1): %v", err)
	}

	want := []string{"9.15.1", "9.18.1"}
	if len(path) != len(want) || path[0] != want[0] || path[1] != want[1] {
		t.Fatalf("FindUpgradePath(9.15.1 -> 9.18.1) = %v, want %v", path, want)
	}
}

func TestFindUpgradePath_NotFound(t *testing.T) {
	edges := map[string][]string{
		"9.0": {"9.1"},
	}

	if _, err := FindUpgradePath(edges, "9.0", "9.99"); err == nil {
		t.Fatal("ожидалась ошибка отсутствия пути, получено nil")
	}
}

func TestValidatePathAgainstCap_Exceeded(t *testing.T) {
	pathContent := readDocFile(t, "Upgrade-path.txt")
	edges, err := ParseUpgradePath(pathContent)
	if err != nil {
		t.Fatalf("ParseUpgradePath: %v", err)
	}

	capContent := readDocFile(t, "Upgrade-cap.txt")
	caps, err := ParseUpgradeCap(capContent)
	if err != nil {
		t.Fatalf("ParseUpgradeCap: %v", err)
	}

	// AFF8040 имеет потолок 9.8, попытка обновить на 9.12.1 должна провалиться.
	path, err := FindUpgradePath(edges, "9.8", "9.12.1")
	if err != nil {
		t.Fatalf("FindUpgradePath: %v", err)
	}

	if err := ValidatePathAgainstCap(path, "AFF8040", caps); err == nil {
		t.Fatal("ожидалась ошибка превышения потолка модели AFF8040, получено nil")
	}
}

func TestValidatePathAgainstCap_OK(t *testing.T) {
	pathContent := readDocFile(t, "Upgrade-path.txt")
	edges, err := ParseUpgradePath(pathContent)
	if err != nil {
		t.Fatalf("ParseUpgradePath: %v", err)
	}

	capContent := readDocFile(t, "Upgrade-cap.txt")
	caps, err := ParseUpgradeCap(capContent)
	if err != nil {
		t.Fatalf("ParseUpgradeCap: %v", err)
	}

	// AFF-A220 имеет потолок 9.18, путь 9.8 -> 9.14.1 должен укладываться.
	path, err := FindUpgradePath(edges, "9.8", "9.14.1")
	if err != nil {
		t.Fatalf("FindUpgradePath: %v", err)
	}

	if err := ValidatePathAgainstCap(path, "AFF-A220", caps); err != nil {
		t.Fatalf("ValidatePathAgainstCap: неожиданная ошибка: %v", err)
	}
}
