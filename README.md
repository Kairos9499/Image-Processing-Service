# Image Processing Service

Cервис для обработки изображений на Go с Protobuf/gRPC API + используется SQLite для хранения состояния заданий и их метаданных.

## Функционал

1. Создание миниатюры (уместить в 512×512)
2. Изменить размер изображения
3. Сделать изображение черно-белым

Программа принимает на вход изображение в формате JPEG или PNG размером до 10 MiB. После обработки доступно сохранение изображения в одном из этих же форматов.

SQLite WAL очередь переживает перезапуск контейнеров. Атомарный захват задания защищает от параллельной повторной обработки несколькими worker.

gRPC health service, корректное завершение API и worker по SIGTERM/SIGINT.

## Запуск

Требуются Docker Engine и Docker Compose v2.

```bash
docker compose up --build
```

API слушает `localhost:50051`. Состояние, исходные изображения и результаты хранятся в Docker volume `image_data`. Остановить сервисы можно командой `docker compose down`; данные останутся в volume. Для удаления данных используйте `docker compose down -v`.

Перед запуском API и worker одноразовый контейнер `init-storage` подготавливает права на общий volume. Приложение при этом продолжает работать от непривилегированного пользователя `app`.

### Запуск CLI-клиента из терминала

После запуска API и worker откройте второй терминал в папке проекта и запустите CLI-клиент на компьютере:

```bash
go run ./cmd/image-client
```

Клиент спросит путь к изображению, операцию и формат, дождётся результата и сохранит его рядом с исходным файлом с суффиксом `_processed`. Для сервера на другом адресе задайте `IMAGE_SERVICE_ADDR` (например, `localhost:50051`). Нужен установленный Go 1.24+.

### Запуск CLI-клиента в контейнере

Запускайте команду из папки проекта. В PowerShell:

```powershell
docker compose --profile client run --rm -it -v "${PWD}:/images" client
```

В командной строке Windows:

```cmd
docker compose --profile client run --rm -it -v "%cd%:/images" client
```

В ответ на запрос пути укажите, например, `/images/photo.png`. Папка проекта подключена к контейнеру для чтения исходника и сохранения результата рядом с ним.

Это монтирование папки, а не копирование: файлы, созданные клиентом в `/images`, сразу появляются в соответствующей папке на компьютере. После завершения контейнера файлы остаются на компьютере.

В обоих случаях каталог слева от `:/images` — текущая папка компьютера. Убедитесь, что исходное изображение находится в ней, и укажите клиенту путь внутри контейнера, например `/images/Pikachu.png`. Результат с суффиксом `_processed` сохранится рядом с исходником.

После окончания работы CLI-контейнер удаляется автоматически благодаря `--rm`. 

## gRPC API

Контракт находится в `api/v1/image_service.proto`, сгенерированный Go-код — в `gen/image/v1`. Для локального вызова можно использовать `grpcurl` с reflection не включённым, передав proto-файл:

```bash
grpcurl -plaintext -import-path . -proto api/v1/image_service.proto \
  -d '{"filename":"photo.png","image":"BASE64_IMAGE_BYTES","operation":"OPERATION_THUMBNAIL","outputFormat":"OUTPUT_FORMAT_PNG"}' \
  localhost:50051 image.v1.ImageService/CreateJob
```

Значение `image` — Base64 от исходных бинарных байтов. `grpcurl` конвертирует JSON Base64 в поле Protobuf `bytes`. В ответе будет `jobId`. Запрашивайте статус:

```bash
grpcurl -plaintext -import-path . -proto api/v1/image_service.proto \
  -d '{"jobId":"JOB_ID"}' localhost:50051 image.v1.ImageService/GetJob
```

После статуса `JOB_STATUS_SUCCEEDED` вызовите `DownloadResult`. Байты и метаданные вернутся в ответе; поле `image` будет Base64. Для `RESIZE` передайте целые `width` и `height` (от 1 до 10000). Для `GRAYSCALE` размеры игнорируются.

## Безопасность и ограничения

- Проверяются размеры изображения до декодирования: максимум 40 млн пикселей (`MAX_PIXELS`).
- Имя файла очищается до базового имени и не используется как путь хранения.

Настройки `DATA_DIR`, `GRPC_ADDR`, `MAX_IMAGE_BYTES`, `MAX_PIXELS` и `WORKER_POLL_INTERVAL` задаются переменными окружения.

## Структура

```text
api/v1/image_service.proto       контракт сервиса
gen/image/v1/                    сгенерированные protobuf/gRPC типы
cmd/image-service/               единая точка входа для режимов api, worker, health
cmd/image-client/                интерактивный клиент для отправки изображений
internal/service/                gRPC методы и валидация входа
internal/processor/              обработка и асинхронный worker
internal/store/                  очередь и состояния в SQLite
Dockerfile                       сборка минимального контейнера
docker-compose.yml               API, worker и постоянный volume
```

## Тесты

Тесты покрывают обработку миниатюры с проверкой результата и перевод повреждённого изображения в `FAILED`.
