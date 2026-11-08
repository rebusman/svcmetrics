# go-musthave-metrics-tpl

Шаблон репозитория для трека «Сервер сбора метрик и алертинга».

## Начало работы

1. Склонируйте репозиторий в любую подходящую директорию на вашем компьютере.
2. В корне репозитория выполните команду `go mod init <name>` (где `<name>` — адрес вашего репозитория на GitHub без префикса `https://`) для создания модуля.

## Конфигурация

Полные примеры JSON-конфигурации: [сервер](config/config.server.example.json) и
[агент](config/config.agent.example.json). Их можно скопировать и изменить под своё окружение.
Запуск с примерами из корня репозитория в отдельных терминалах:

```sh
go run ./cmd/server -c config/config.server.example.json
go run ./cmd/agent -c config/config.agent.example.json
```

Путь к файлу задаётся через `-c`, `-config` или переменную окружения `CONFIG`.
Непустая `CONFIG` имеет приоритет над флагами выбора файла. Для самих настроек
приоритет такой: переменные окружения → явно заданные флаги → JSON → значения по умолчанию.
Поля JSON можно опускать; неизвестные поля игнорируются.

| Поле | Где используется | Тип и формат |
|---|---|---|
| `address` | сервер, агент | Строка: `localhost:8080`; у агента также допустим URL `http://localhost:8080` |
| `store_interval` | сервер | Целое число секунд или строка длительности Go, например `300`, `"5m"`, `"500ms"`; `0` сохраняет после каждого обновления |
| `store_file` | сервер | Строка: путь к файлу метрик, например `metrics.json`; пустая строка отключает сохранение в файл |
| `restore` | сервер | Boolean: `true` или `false`, восстанавливать ли метрики из файла при запуске |
| `database_dsn` | сервер | Строка подключения PostgreSQL, например `postgres://metrics:example@localhost:5432/metrics?sslmode=disable`; пустая строка выбирает хранение в памяти или файле |
| `audit_file` | сервер | Строка: путь к файлу аудита, например `audit.jsonl`; пустая строка отключает этот получатель |
| `audit_url` | сервер | Строка: URL получателя событий, например `http://localhost:9090/audit`; пустая строка отключает этот получатель |
| `report_interval` | агент | Целое число секунд или строка длительности Go, например `10` или `"10s"` |
| `poll_interval` | агент | Целое число секунд или строка длительности Go, например `2` или `"2s"` |
| `batch_size` | агент | Целое число: количество метрик в запросе, например `32` |
| `rate_limit` | агент | Целое число: количество одновременных запросов, например `1` |
| `key` | сервер, агент | Строка: общий секрет HMAC; пустая строка отключает подпись |
| `crypto_key` | сервер, агент | Строка: путь к PEM RSA-ключу — приватному у сервера (`private.pem`), публичному у агента (`public.pem`); пустая строка отключает шифрование |

В примерах ключи, PostgreSQL и аудит отключены. Сервер сохраняет метрики в
`metrics.json` каждые 300 секунд и восстанавливает их при запуске.
Чтобы хранить метрики только в памяти, задайте `"store_file": ""`.
При непустом `database_dsn` сервер использует PostgreSQL вместо памяти и файла.

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
$ go tool pprof -top -nodecount=12 -diff_base= profiles/base.pprof profiles/result.pprof
Showing nodes accounting for -1819.46MB, 94.67% of 1921.82MB total
      flat  flat%   sum%        cum   cum%
-1300.43MB 67.67% 67.67% -1581.14MB 82.27%  compress/flate.NewWriter (inline)
 -272.93MB 14.20% 81.87%  -272.93MB 14.20%  compress/flate.(*compressor).initDeflate (inline)
 -123.56MB  6.43% 88.30%  -123.56MB  6.43%  compress/flate.(*dictDecoder).init (inline)
  -33.78MB  1.76% 90.06%   -33.78MB  1.76%  sync.(*Pool).pinSlow
  -29.08MB  1.51% 91.57%  -152.64MB  7.94%  compress/flate.NewReader
  -24.62MB  1.28% 92.85%   -24.62MB  1.28%  net/http.init.func15
  -15.81MB  0.82% 93.67%   -15.81MB  0.82%  bufio.NewReaderSize (inline)
  -13.55MB   0.7% 94.38%   -13.55MB   0.7%  compress/flate.(*huffmanEncoder).generate
   -2.93MB  0.15% 94.53%    -9.91MB  0.52%  encoding/json.Marshal
   -2.69MB  0.14% 94.67%  -173.10MB  9.01%  compress/gzip.NewReader (inline)
   -0.05MB 0.0024% 94.67% -1552.44MB 80.78%  github.com/rebusman/svcmetrics/internal/handler.GzipResponseMiddleware.func1
   -0.03MB 0.0016% 94.67%  -147.64MB  7.68%  github.com/rebusman/svcmetrics/internal/agent.(*Agent).sendBatch
```


```
$ go tool pprof -top -sample_index=alloc_objects -focus=aggregateBatch -diff_base profiles/base.pprof profiles/result.pprof
Showing nodes accounting for -30003, 4.12% of 728034 total
      flat  flat%   sum%        cum   cum%
    -28003  3.85%  3.85%     -28003  3.85%  github.com/rebusman/svcmetrics/internal/repository.copyMetric (inline)
     -4000  0.55%  4.40%      -4000  0.55%  internal/reflectlite.Swapper
      2000  0.27%  4.12%     -30003  4.12%  github.com/rebusman/svcmetrics/internal/repository.aggregateBatch
```


## Шифрование сообщений агента

У агента `-crypto-key` (или `CRYPTO_KEY`) задаёт путь к публичному RSA-ключу,
у сервера — к приватному. Непустая переменная окружения имеет приоритет над флагом.
Ключи загружаются при запуске; ошибка чтения или формата останавливает запуск.
Без ключей сохраняется прежний режим обмена.

Создание пары ключей (OpenSSL):

```sh
openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 -out private.pem
openssl pkey -in private.pem -pubout -out public.pem
```

Запуск в отдельных терминалах:

```sh
go run ./cmd/server -crypto-key private.pem
go run ./cmd/agent -crypto-key public.pem
```

Поддерживаются PEM-ключи RSA размером от 2048 бит: публичные PKIX/PKCS#1,
приватные PKCS#8/PKCS#1, без пароля. Приватный ключ хранится только на сервере.

Агент сжимает JSON с помощью gzip, шифрует его AES-256-GCM со случайными ключом
и nonce для каждого запроса, затем шифрует ключ AES публичным ключом RSA-OAEP
(SHA-256). Тело содержит ключ AES в зашифрованном виде, nonce и шифротекст GCM.
Заголовок `X-Payload-Encryption: RSA-OAEP-AES-256-GCM` обозначает этот формат.
При настроенном `KEY` HMAC покрывает итоговое зашифрованное тело.
Сервер проверяет HMAC, расшифровывает тело и распаковывает gzip.
Повреждённые сообщения и сообщения с неподходящим ключом отклоняются с HTTP 400;
размер зашифрованного тела ограничен 10 МиБ (HTTP 413 при превышении).
Сервер также принимает обычные запросы без заголовка шифрования; ответы не шифруются.
