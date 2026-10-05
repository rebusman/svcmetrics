# go-musthave-metrics-tpl

Шаблон репозитория для трека «Сервер сбора метрик и алертинга».

## Начало работы

1. Склонируйте репозиторий в любую подходящую директорию на вашем компьютере.
2. В корне репозитория выполните команду `go mod init <name>` (где `<name>` — адрес вашего репозитория на GitHub без префикса `https://`) для создания модуля.

## Обновление шаблона

Чтобы иметь возможность получать обновления автотестов и других частей шаблона, выполните команду:

```
git remote add -m v2 template https://github.com/Yandex-Practicum/go-musthave-metrics-tpl.git
```

Для обновления кода автотестов выполните команду:

```
git fetch template && git checkout template/v2 .github
```

Затем добавьте полученные изменения в свой репозиторий.

## Запуск автотестов

Для успешного запуска автотестов называйте ветки `iter<number>`, где `<number>` — порядковый номер инкремента. Например, в ветке с названием `iter4` запустятся автотесты для инкрементов с первого по четвёртый.

При мёрже ветки с инкрементом в основную ветку `main` будут запускаться все автотесты.

Подробнее про локальный и автоматический запуск читайте в [README автотестов](https://github.com/Yandex-Practicum/go-autotests).

## Структура проекта

Приведённая в этом репозитории структура проекта является рекомендуемой, но не обязательной.

Это лишь пример организации кода, который поможет вам в реализации сервиса.

При необходимости можно вносить изменения в структуру проекта, использовать любые библиотеки и предпочитаемые структурные паттерны организации кода приложения, например:
- **DDD** (Domain-Driven Design)
- **Clean Architecture**
- **Hexagonal Architecture**
- **Layered Architecture**


## Бенчмарки и оптимизация памяти

### Бенчмарки

Бенчмарки покрывают ключевые компоненты системы:

| Пакет | Бенчмарки | Что измеряют |
|---|---|---|
| `internal/repository` | `BenchmarkMemStorage*`, `BenchmarkAggregateBatch` | запись и чтение in-memory хранилища, валидация и свёртка батча |
| `internal/handler` | `BenchmarkUpdatesJSONHandler`, `BenchmarkUpdateJSONHandler`, `BenchmarkValueJSONHandler`, `BenchmarkListHandler`, `BenchmarkSignedGzipBatch` | обработчики и цепочка «проверка подписи → распаковка → обработчик → сжатие → подпись ответа» |
| `internal/hashing` | `BenchmarkSum`, `BenchmarkEqual` | HMAC-SHA256 подпись и её проверка |
| `internal/agent` | `BenchmarkCollectRuntimeMetrics`, `BenchmarkCollectBatch`, `BenchmarkSendBatch`, `BenchmarkGzipCompress*` | сбор метрик, формирование и отправка батча |
| `cmd/server` | `BenchmarkAgentReport` | вся система: агент собирает метрики и отправляет подписанный gzip-батч в production-роутер сервера, который его проверяет, распаковывает, сохраняет и пишет в аудит |

Запуск:

```
go test -run '^$' -bench . -benchmem ./...
```

### Профилирование

Профили памяти сняты со сквозного бенчмарка `BenchmarkAgentReport`. Число итераций фиксировано: суммарный объём аллокаций растёт вместе с ним, и без фиксации профили `base` и `result` нельзя было бы сравнить. `-memprofilerate 1` записывает каждую аллокацию, а не выборку:

```
go test -run '^$' -bench BenchmarkAgentReport -benchmem -benchtime 2000x     -memprofilerate 1 -memprofile profiles/base.pprof ./cmd/server
```

`profiles/result.pprof` снят той же командой после оптимизации.

### Что нашёл профиль и что исправлено

Анализ проводился командами `top`, `peek`, `list` в `go tool pprof profiles/base.pprof`:

1. **`compress/flate.NewWriter` — 83 % всей выделенной памяти.** `GzipResponseMiddleware` создавал новый gzip-компрессор (около 800 КБ таблиц) на каждый ответ, даже на `{"status":"ok"}` из 15 байт. Компрессоры теперь берутся из `sync.Pool`.
2. **`compress/gzip.NewReader` — 9 %.** Половину создавал `GzipRequestMiddleware` на каждый запрос: декомпрессоры теперь тоже в пуле, вместе с `bufio.Reader`, в который `gzip.Reader.Reset` иначе каждый раз заворачивает тело запроса. Вторую половину создавал HTTP-клиент агента, распаковывая ответ сервера, который агент даже не читает: агент теперь отправляет `Accept-Encoding: identity`, и сервер не сжимает ему ответ.
3. **`repository.aggregateBatch`** выделял по отдельному `float64`/`int64` на каждую метрику батча и использовал `sort.Slice` (рефлексия). Значения теперь лежат в двух слайсах, выделенных разом, сортировка — `slices.SortFunc`: 39 аллокаций на батч из 32 метрик → 6.
4. **Агент:** `CollectRuntimeMetrics` строил и выбрасывал промежуточную карту на каждом опросе — теперь значения пишутся прямо в состояние (936 B → 0). `collectBatch` копировал карту gauge-метрик, отдельно сортировал ключи и выделял по указателю на каждую метрику — теперь батч собирается прямо из состояния (45 аллокаций → 3). JSON батча кодируется сразу в gzip-компрессор без промежуточного буфера, URL `/updates/` строится один раз, а не через `fmt.Sprintf` на каждую отправку.

### Результаты бенчмарков

| Бенчмарк | До | После |
|---|---|---|
| `BenchmarkAgentReport` | 452 µs, 1 149 323 B, 451 allocs | 195 µs, 40 125 B, 302 allocs |
| `BenchmarkSignedGzipBatch` | 132 µs, 881 082 B, 222 allocs | 40 µs, 30 139 B, 165 allocs |
| `BenchmarkUpdatesJSONHandler` | 21 µs, 18 301 B, 172 allocs | 20 µs, 18 438 B, 139 allocs |
| `BenchmarkAggregateBatch` | 3.8 µs, 4 896 B, 39 allocs | 3.2 µs, 5 032 B, 6 allocs |
| `BenchmarkCollectRuntimeMetrics` | 11.8 µs, 936 B, 3 allocs | 10.6 µs, 0 B, 0 allocs |
| `BenchmarkCollectBatch` | 3.2 µs, 3 952 B, 45 allocs | 1.4 µs, 1 640 B, 3 allocs |

### Сравнение профилей

```
go tool pprof -top -diff_base=profiles/base.pprof profiles/result.pprof
```

`