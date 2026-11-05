package upgrade

import (
	"bufio"
	"fmt"
	"strings"
)

// ParseUpgradeCap парсит содержимое Upgrade-cap.txt в карту model -> latest supported version.
// Игнорирует заголовочные строки. Дедуплицирует модели, оставляя максимальную
// поддерживаемую версию среди повторяющихся записей.
func ParseUpgradeCap(content string) (map[string]string, error) {
	caps := make(map[string]string)

	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) != 2 {
			// заголовочные строки вида "model ontap support." или "model latest supported ontap version"
			continue
		}

		model, version := fields[0], fields[1]
		if !looksLikeVersion(version) {
			continue
		}

		existing, ok := caps[model]
		if !ok || CompareVersions(version, existing) > 0 {
			caps[model] = version
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("ошибка чтения Upgrade-cap.txt: %w", err)
	}

	return caps, nil
}

// ValidatePathAgainstCap проверяет, что ни одна версия из path (включая
// промежуточные и целевую) не превышает потолок поддержки модели model,
// заданный в caps. Возвращает ошибку при первом превышении.
func ValidatePathAgainstCap(path []string, model string, caps map[string]string) error {
	cap, ok := caps[model]
	if !ok {
		return fmt.Errorf("модель %q отсутствует в Upgrade-cap.txt", model)
	}

	for _, v := range path {
		if CompareVersions(v, cap) > 0 {
			return fmt.Errorf("model upgrade cap exceeded: версия %s превышает потолок %s для модели %s", v, cap, model)
		}
	}

	return nil
}
