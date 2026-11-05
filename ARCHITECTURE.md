# 1. Анализ архитектурных подходов

## Вариант 1: Go-бинарник (встроенный HTTP-сервер, FSM со стейт-файлом state.json, cross-platform)

**Архитектурный паттерн и рантайм**
- Монолитный статически скомпонованный Go-бинарник (CGO_ENABLED=0), внутри — четыре подсистемы: serial (COM-порты), expect (движок ожидания промптов), httpd (встроенный net/http для отдачи образов ONTAP), fsm (конечный автомат с персистентным state.json).
- Windows air-gap (RDP): копируется ontap-auto.exe + папка images/ + config.yaml. Никаких зависимостей, никакого Python, никакого внешнего nginx.
- Linux air-gap: тот же бинарник, собранный под linux/amd64, запускается как systemd-юнит или напрямую.
- Конфиг config.yaml: COM-порты A/B, IP/маска/шлюз для e0M, URL образа, целевые версии, имена узлов, cluster-IP.

**Оркестрация ступенчатой прошивки HA-пары**
- FSM — набор независимых подграфов по нодам (nodeA, nodeB). Каждый подграф — явный граф состояний: loader -> boot menu -> пункт 7 (e0M, IP, маска, шлюз, HTTP URL) -> ожидание загрузки ONTAP -> CLI.
- Строго последовательны только две операции:
  - прошивка через Boot Menu -> пункт 7 (нода A полностью, затем нода B);
  - 9b (wipeconfig + init) — сначала на ноде A, затем на ноде B.
  Эти шаги помечаются в FSM как serial_barrier и не могут выполняться параллельно.
- Всё остальное — параллелизуемо:
  - вход в Loader и boot_ontap menu на обеих нодах;
  - 9a (destroy aggregates + unpartition) — независим на каждой ноде;
  - ожидание загрузки ONTAP и CLI prompt;
  - настройка SP (system service-processor network modify) и e0M LIF — независимы по нодам;
  - cluster create — выполняется только на LOADER-A (нода B присоединяется автоматически).
- state.json хранит: {nodes: {A: {step, in_flight}, B: {...}}, barriers: {firmware: done|pending, wipeconfig: done|pending}}.

**Поведение при аварийном завершении процесса**
- state.json пишется атомарно (write tmp -> fsync -> rename) на каждом переходе FSM.
- При перезапуске бинарник читает стейт, переоткрывает COM-порты и пассивно читает вывод консоли для распознавания текущего состояния.
- При in_flight=true FSM не повторяет разрушительную команду, а ждет подтверждения оператора.

---

## Вариант 2: Python standalone (внешний HTTP-сервер, линейный пайплайн)

**Архитектурный паттерн и рантайм**
- Упаковка через PyInstaller в exe / zipapp. Внутри — pyserial, requests, линейные вызовы.
- Требует запуска внешнего процесса HTTP-сервера для раздачи images/.
- Риск зависимостей PyInstaller в закрытом контуре Windows (антивирус, VC++ runtime).

**Оркестрация и восстановление**
- Жесткая линейная очередь: нода A целиком, затем нода B.
- При сбое очередь останавливается, FSM-пересчета по промпту нет, требуется ручное вмешательство.

---

## Вариант 3: Двухфазный агент (Фаза 1: COM/Expect -> Фаза 2: SSH/API)

**Архитектурный паттерн и рантайм**
- Фаза 1: COM-порты до момента поднятия сети нодой.
- Фаза 2: SSH-подключение к e0M/SP для выполнения cluster create и настроек сети.
- Высокий риск: если сеть на ноде не поднялась или link-local 169.254.x.x недоступен, агент зависает между фазами.

---

## Сводная таблица компромиссов (Trade-off Matrix)

| Критерий | Вариант 1 (Go FSM) | Вариант 2 (Python Standalone) | Вариант 3 (Двухфазный агент) |
|---|---|---|---|
| **Автономность** | Высокая — один exe + images, 0 зависимостей | Средняя — PyInstaller + внешний HTTP | Высокая — один exe, но зависимость от SSH |
| **Надежность прошивки** | Высокая — FSM, barriers, атомарный state.json | Средняя — линейный скрипт без FSM | Высокая — state.json на обеих фазах |
| **Сложность отладки** | Средняя — логи FSM + чтение state.json | Низкая — линейный вывод | Высокая — стык двух протоколов (COM + SSH) |
| **Риск окирпичивания** | Низкий — in_flight флаги защищают от повтора команд | Средний — ручной разбор при падении | Средний — зависимость от сетевого стека |

---

# 2. Анализ отказоустойчивости и рисков (Pre-Mortem)

## Сбой 1: Normal Boot is prohibited после wipeconfig
- **Детекция:** Regex `Normal Boot is prohibited\.` и `The boot device has changed\.` (таймаут 10 минут).
- **Предохранитель:** FSM блокирует отправку пункта 1 (Normal Boot), выбирает 9a -> 9b или 4 (init) согласно state.json. Если 9b уже был — аварийный останов.

## Сбой 2: Failed disk detected during disk initialization (сбойный диск 0a.14.7)
- **Детекция:** Regex `Failed disk (\S+) detected` и `The disk (\S+) was skipped because it reported the status disk failed`.
- **Предохранитель:** Сбойный диск фиксируется в state.json, работа продолжается. Если сбойных дисков >= 2 — останов (для root aggregate 3d+2p+1s нужно минимум 6 дисков). Если 1 диск — статус degraded.

## Сбой 3: Рассинхронизация 9a/9b между нодами HA-пары
- **Детекция:** Regex `Has option \(9a\) been completed on all the nodes \(yes/no\)\?`.
- **Предохранитель:** Горутина ноды A ждет через `serial_barrier` ноду B. На вопрос визарда посылается `yes` ТОЛЬКО после того, как обе ноды подтвердили прохождение 9a.

## Сбой 4: Нода B зависла в post-cluster-setup после cluster create
- **Детекция:** Парсинг вывода `cluster add-node-status` (статус post-cluster-setup вместо success).
- **Предохранитель:** FSM поллит статус каждые 30 сек (таймаут 10 мин). К настройке SP/e0M не переходит, пока обе ноды не будут в success. Повторный cluster create заблокирован.

## Сбой 5: Обрыв RDP / краш процесса посреди разрушительной операции
- **Детекция:** При старте бинарник видит флаг `in_flight=true` в state.json.
- **Предохранитель:** Разрушительная команда НИКОГДА не повторяется вслепую. FSM делает read-only проверку (vol status, cluster show). Если операция прошла — помечает done, если нет — требует подтверждения оператора.

---

# 3. Техническая спецификация и контракты реализации

## 3.1. Схема входного config.yaml

```go
// Config — корневая структура конфигурации, загружается из config.yaml при старте.
type Config struct {
	Cluster   ClusterConfig `yaml:"cluster"`
	Nodes     NodesConfig   `yaml:"nodes"`
	HTTP      HTTPConfig    `yaml:"http"`
	Upgrade   UpgradeConfig `yaml:"upgrade"`
}

// ClusterConfig — параметры создаваемого кластера.
type ClusterConfig struct {
	Name        string `yaml:"name"`         // имя кластера, напр. "cluster1"
	MgmtIP      string `yaml:"mgmt_ip"`      // cluster management IP
	MgmtMask    string `yaml:"mgmt_mask"`    // напр. "255.255.255.0"
	MgmtGateway string `yaml:"mgmt_gateway"`
}

// NodesConfig — параметры обеих нод HA-пары.
type NodesConfig struct {
	NodeA NodeConfig `yaml:"node_a"`
	NodeB NodeConfig `yaml:"node_b"`
}

// NodeConfig — параметры одной ноды: COM-порт, e0M, Service Processor.
type NodeConfig struct {
	Name     string         `yaml:"name"`      // напр. "node1"
	Serial   SerialConfig   `yaml:"serial"`
	E0M      NetworkConfig  `yaml:"e0m"`
	SP       SPConfig       `yaml:"sp"`
}

// SerialConfig — настройки COM-порта для данной ноды.
type SerialConfig struct {
	Port     string `yaml:"port"`      // напр. "COM3"
	BaudRate int    `yaml:"baud_rate"` // дефолт 9600 (Loader/Boot Menu), 115200 (console после загрузки)
	DataBits int    `yaml:"data_bits"` // дефолт 8
	Parity   string `yaml:"parity"`    // "N" | "E" | "O", дефолт "N"
	StopBits int    `yaml:"stop_bits"` // дефолт 1
}

// NetworkConfig — IP-параметры интерфейса (e0M).
type NetworkConfig struct {
	IP      string `yaml:"ip"`
	Mask    string `yaml:"mask"`
	Gateway string `yaml:"gateway"`
}

// SPConfig — параметры Service Processor (SP network modify).
type SPConfig struct {
	IP      string `yaml:"ip"`
	Mask    string `yaml:"mask"`
	Gateway string `yaml:"gateway"`
}

// HTTPConfig — встроенный HTTP-сервер раздачи образов ONTAP.
type HTTPConfig struct {
	ListenAddr string `yaml:"listen_addr"` // напр. "0.0.0.0:8080"
	ImagesDir  string `yaml:"images_dir"`  // напр. "./images"
}

// UpgradeConfig — целевая версия ONTAP и имя файла образа.
type UpgradeConfig struct {
	TargetVersion string `yaml:"target_version"` // напр. "9.13.1"
	ImageFile     string `yaml:"image_file"`      // имя файла в ImagesDir, отдаётся по HTTP
}
```

Дефолты, применяемые при отсутствии значения в YAML:
- `baud_rate`: `9600` для сессии Loader/Boot Menu, `115200` после входа в ONTAP CLI (переключение baud rate — явный шаг FSM, не настраивается пользователем).
- `data_bits`: `8`, `parity`: `"N"`, `stop_bits`: `1`.
- `http.listen_addr`: `"0.0.0.0:8080"`.

## 3.2. Схема состояния state.json

```json
{
  "version": 1,
  "nodes": {
    "A": {
      "current_step": "boot_menu_option_7",
      "last_prompt": "LOADER-A>",
      "in_flight": false,
      "failed_disks": ["0a.14.7"]
    },
    "B": {
      "current_step": "awaiting_ontap_boot",
      "last_prompt": "::>",
      "in_flight": false,
      "failed_disks": []
    }
  },
  "barriers": {
    "firmware_option_7": {
      "name": "serial_barrier",
      "node_a_done": true,
      "node_b_done": false
    },
    "wipeconfig_9b": {
      "name": "serial_barrier",
      "node_a_done": false,
      "node_b_done": false
    }
  },
  "cluster": {
    "create_issued": false,
    "add_node_status_a": "pending",
    "add_node_status_b": "pending"
  }
}
```

Соответствующие Go-структуры:

```go
// State — корневая структура файла state.json.
type State struct {
	Version  int                 `json:"version"`
	Nodes    map[string]NodeState `json:"nodes"`   // ключи: "A", "B"
	Barriers map[string]Barrier  `json:"barriers"` // ключи: "firmware_option_7", "wipeconfig_9b"
	Cluster  ClusterState        `json:"cluster"`
}

// NodeState — прогресс конкретной ноды.
type NodeState struct {
	CurrentStep string   `json:"current_step"` // напр. "boot_menu_option_7", "awaiting_ontap_boot"
	LastPrompt  string   `json:"last_prompt"`  // последний распознанный промпт консоли
	InFlight    bool     `json:"in_flight"`    // true во время выполнения разрушительной команды
	FailedDisks []string `json:"failed_disks"` // напр. ["0a.14.7"]
}

// Barrier — точка синхронизации между нодами HA-пары.
type Barrier struct {
	Name       string `json:"name"` // "serial_barrier"
	NodeADone  bool   `json:"node_a_done"`
	NodeBDone  bool   `json:"node_b_done"`
}

// ClusterState — прогресс создания кластера и присоединения узлов.
type ClusterState struct {
	CreateIssued     bool   `json:"create_issued"`
	AddNodeStatusA   string `json:"add_node_status_a"` // "pending" | "post-cluster-setup" | "success"
	AddNodeStatusB   string `json:"add_node_status_b"`
}
```

Запись state.json выполняется атомарно: сериализация во временный файл в той же директории, `fsync`, затем `os.Rename` на целевой путь.

## 3.3. Таблица регулярных выражений (Expect Engine)

| Категория | Назначение | Regex |
|---|---|---|
| Промпт | LOADER | `^LOADER-[AB]>\s*$` |
| Промпт | Boot Menu, выбор пункта | `Selection \(1-11\)\?\s*$` |
| Промпт | ONTAP CLI (node shell) | `\S+::>\s*$` |
| Промпт | ONTAP CLI (без имени узла) | `::>\s*$` |
| Промпт | Выход из cluster setup wizard | `Exiting the cluster setup wizard\.` |
| Событие | Запрет Normal Boot после wipeconfig | `Normal Boot is prohibited\.` |
| Событие | Смена boot-девайса | `The boot device has changed\.` |
| Событие | Сбойный диск обнаружен | `Failed disk (\S+) detected` |
| Событие | Диск пропущен как failed | `The disk (\S+) was skipped because it reported the status disk failed` |
| Вопрос визарда | Подтверждение 9a на всех нодах | `Has option \(9a\) been completed on all the nodes \(yes/no\)\?\s*$` |
| Вопрос визарда | Общий yes/no | `\(yes/no\)\?\s*$` |
| Вопрос визарда | Общий y/n | `\(y/n\)\?\s*$` |
| Статус ноды в кластере | Промежуточный этап | `post-cluster-setup` |
| Статус ноды в кластере | Успешное присоединение | `success` |

Все regex компилируются один раз при старте через `regexp.MustCompile` и применяются построчно к буферу вывода COM-порта.

## 3.4. Алгоритм обхода графа прошивок (DAG)

**Нормализация версии.**
Версия из `cluster image show` или из конфигурации приходит в формате `9.13.1P6` или `9.8P1`. Нормализация отсекает суффикс патча `Pxx`, оставляя базовую версию `major.minor[.patch]`:

```go
// normalizeVersion отсекает суффикс вида "P6" и возвращает базовую версию ONTAP.
// "9.13.1P6" -> "9.13.1"; "9.8P1" -> "9.8".
func normalizeVersion(raw string) string {
	re := regexp.MustCompile(`^(\d+(?:\.\d+)*)P\d+$`)
	if m := re.FindStringSubmatch(raw); m != nil {
		return m[1]
	}
	return raw
}
```

**Построение графа.** Файл `Upgrade-path.txt` парсится в ориентированный граф `from -> to`, где каждая строка задаёт ребро максимально допустимого прямого перехода. Строка `9.15.1 any higher` трактуется как ребро в условный узел "без верхнего ограничения" (терминальная версия графа, равная целевой версии при поиске пути, если текущая версия >= 9.15.1).

**Поиск пути.** Используется BFS от нормализованной текущей версии до нормализованной целевой версии, так как граф — DAG с единичным весом ребра (каждый переход — одна операция обновления):

```go
// FindUpgradePath возвращает последовательность промежуточных версий
// от текущей до целевой включительно, используя ребра edges (from -> []to).
// Возвращает ошибку, если путь не существует.
func FindUpgradePath(edges map[string][]string, from, to string) ([]string, error) {
	from = normalizeVersion(from)
	to = normalizeVersion(to)

	type queueItem struct {
		version string
		path    []string
	}
	visited := map[string]bool{from: true}
	queue := []queueItem{{version: from, path: []string{from}}}

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur.version == to {
			return cur.path, nil
		}
		for _, next := range edges[cur.version] {
			if visited[next] {
				continue
			}
			visited[next] = true
			queue = append(queue, queueItem{version: next, path: append(append([]string{}, cur.path...), next)})
		}
	}
	return nil, fmt.Errorf("upgrade path not found: %s -> %s", from, to)
}
```

**Валидация потолка модели.** Перед выполнением найденного пути каждый промежуточный шаг проверяется против `Upgrade-cap.txt`: версия шага не должна превышать `latest supported ontap version` для модели контроллера, указанной в конфигурации (`Controller-A.txt` / `Controller-B.txt`). Если промежуточная версия пути превышает потолок модели — путь считается невалидным, FSM останавливается с ошибкой `model upgrade cap exceeded` до вмешательства оператора.

## 3.5. Структура пакетов Go

```
.
├── cmd/
│   └── provisioner/
│       └── main.go              # точка входа, разбор флагов, загрузка config.yaml
├── internal/
│   ├── serial/
│   │   ├── session.go           # реализация ConsoleSession поверх COM-порта
│   │   └── session_test.go
│   ├── expect/
│   │   ├── patterns.go          # таблица regex из 3.3
│   │   └── engine.go            # ожидание промптов/событий, таймауты
│   ├── fsm/
│   │   ├── state.go             # структуры State/NodeState/Barrier (3.2)
│   │   ├── store.go             # атомарная чтение/запись state.json
│   │   ├── node_states.go       # реализации FSMState для каждого шага ноды
│   │   └── barrier.go           # логика serial_barrier
│   ├── httpd/
│   │   └── server.go            # встроенный net/http для раздачи images_dir
│   └── upgrade/
│       ├── graph.go             # парсинг Upgrade-path.txt, FindUpgradePath (3.4)
│       ├── cap.go                # парсинг Upgrade-cap.txt, валидация потолка модели
│       └── version.go            # normalizeVersion и сопутствующие хелперы
├── config.yaml
├── images/
└── go.mod
```

**Интерфейс ConsoleSession** (пакет `internal/serial`, используется `internal/expect` и `internal/fsm`):

```go
// ConsoleSession — абстракция над физическим или тестовым COM-портом.
type ConsoleSession interface {
	// Write отправляет данные (команду, ответ на вопрос визарда) в консоль.
	Write(data []byte) (int, error)
	// ReadUntil блокируется до совпадения вывода с одним из regex из patterns
	// либо до истечения timeout, возвращает совпавший текст и индекс паттерна.
	ReadUntil(patterns []*regexp.Regexp, timeout time.Duration) (matched string, patternIdx int, err error)
	// Close закрывает COM-порт.
	Close() error
}
```

**Интерфейс FSMState** (пакет `internal/fsm`, по одной реализации на шаг конечного автомата, напр. `LoaderState`, `BootMenuOption7State`, `WipeconfigState`):

```go
// FSMState — один шаг конечного автомата прошивки ноды.
type FSMState interface {
	// Name возвращает идентификатор шага, записываемый в NodeState.CurrentStep.
	Name() string
	// Run выполняет шаг: отправляет команды через ConsoleSession,
	// ожидает промпты/события, обновляет st (NodeState) и возвращает следующий шаг.
	// Перед разрушительной операцией обязан выставить st.InFlight = true
	// и сохранить state.json до отправки команды.
	Run(ctx context.Context, session serial.ConsoleSession, st *NodeState) (next FSMState, err error)
}
```
