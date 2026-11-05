package upgrade

import (
	"regexp"
	"strconv"
	"strings"
)

var patchSuffixRe = regexp.MustCompile(`^(\d+(?:\.\d+)*)P\d+$`)

// NormalizeVersion отсекает суффикс вида "P6" и возвращает базовую версию ONTAP.
// "9.13.1P6" -> "9.13.1"; "9.8P1" -> "9.8".
func NormalizeVersion(raw string) string {
	raw = strings.TrimSpace(raw)
	if m := patchSuffixRe.FindStringSubmatch(raw); m != nil {
		return m[1]
	}
	return raw
}

// CompareVersions сравнивает две версии ONTAP посегментно (major.minor.patch...).
// Возвращает -1, если v1 < v2; 0, если равны; 1, если v1 > v2.
// Недостающие сегменты считаются нулём, т.е. "9.8" == "9.8.0".
func CompareVersions(v1, v2 string) int {
	s1 := strings.Split(NormalizeVersion(v1), ".")
	s2 := strings.Split(NormalizeVersion(v2), ".")

	n := len(s1)
	if len(s2) > n {
		n = len(s2)
	}

	for i := 0; i < n; i++ {
		a := segmentAt(s1, i)
		b := segmentAt(s2, i)
		if a != b {
			if a < b {
				return -1
			}
			return 1
		}
	}
	return 0
}

func segmentAt(segments []string, idx int) int {
	if idx >= len(segments) {
		return 0
	}
	v, err := strconv.Atoi(segments[idx])
	if err != nil {
		return 0
	}
	return v
}
