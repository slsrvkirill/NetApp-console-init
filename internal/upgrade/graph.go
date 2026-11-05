package upgrade

import (
	"bufio"
	"fmt"
	"strings"
)

// anyHigherKey — специальный ключ в карте рёбер, под которым хранится
// минимальная версия, начиная с которой действует правило "any higher"
// (из строки "9.15.1 any higher" в Upgrade-path.txt).
const anyHigherKey = "any_higher_from"

// ParseUpgradePath парсит содержимое Upgrade-path.txt в ориентированный граф
// from -> []to. Игнорирует заголовок и пустые строки. Строка "9.15.1 any higher"
// трактуется как специальное правило перехода на любую версию >= 9.15.1
// и сохраняется под ключом anyHigherKey.
func ParseUpgradePath(content string) (map[string][]string, error) {
	edges := make(map[string][]string)

	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) != 2 {
			// заголовочные строки вида "maximum ontap versions ..." или "FROM TO"
			continue
		}

		from, to := fields[0], fields[1]
		if !looksLikeVersion(from) {
			continue
		}

		if strings.EqualFold(to, "higher") || strings.EqualFold(to, "any") {
			// на случай разбиения "any higher" на отдельные поля
			edges[anyHigherKey] = []string{from}
			continue
		}

		edges[from] = append(edges[from], to)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("ошибка чтения Upgrade-path.txt: %w", err)
	}

	return edges, nil
}

func looksLikeVersion(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && r != '.' {
			return false
		}
	}
	return true
}

// FindUpgradePath возвращает последовательность промежуточных версий
// от текущей до целевой включительно, используя ребра edges (from -> []to).
// Учитывает правило "any higher": из любой версии >= anyHigherFrom
// существует прямое ребро в целевую версию to.
// Возвращает ошибку, если путь не существует.
func FindUpgradePath(edges map[string][]string, from, to string) ([]string, error) {
	from = NormalizeVersion(from)
	to = NormalizeVersion(to)

	anyHigherFrom := ""
	if v, ok := edges[anyHigherKey]; ok && len(v) > 0 {
		anyHigherFrom = v[0]
	}

	if from == to {
		return []string{from}, nil
	}

	type queueItem struct {
		version string
		path    []string
	}

	visited := map[string]bool{from: true}
	queue := []queueItem{{version: from, path: []string{from}}}

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]

		neighbors := append([]string{}, edges[cur.version]...)
		if anyHigherFrom != "" && CompareVersions(cur.version, anyHigherFrom) >= 0 {
			neighbors = append(neighbors, to)
		}

		for _, next := range neighbors {
			if next == to {
				return append(append([]string{}, cur.path...), to), nil
			}
			if visited[next] {
				continue
			}
			visited[next] = true
			newPath := append(append([]string{}, cur.path...), next)
			queue = append(queue, queueItem{version: next, path: newPath})
		}
	}

	return nil, fmt.Errorf("upgrade path not found: %s -> %s", from, to)
}
