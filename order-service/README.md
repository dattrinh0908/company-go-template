# order-service

Bản triển khai tham chiếu cho Go service template của công ty: một HTTP service
phân tầng với chiều phụ thuộc rõ ràng, structured logging, tracing và metrics
qua OpenTelemetry, cùng chiến lược test mà phần lớn không cần đến database.

## Cấu trúc thư mục

```
cmd/api/main.go              composition root: dựng mọi tầng, quản lý vòng đời process
internal/
  config/                    đọc và validate biến môi trường (nơi duy nhất gọi os.Getenv)
  domain/                    entity, invariant, port, use case — không import framework
  repository/                bản cài đặt PostgreSQL cho các port của domain
  transport/http/            router Gin, middleware, handler, ánh xạ lỗi
  observability/             logger, tracer/meter provider, metric RED
migrations/                  các cặp .up.sql / .down.sql đánh số thứ tự
```

## Quy tắc duy nhất

Phụ thuộc luôn hướng vào trong. `transport` và `repository` đều import `domain`;
`domain` không import cái nào trong hai.

```
transport/http  ─┐
                 ├─→  domain  ←─  (port khai báo ở đây)
repository      ─┘                        ↑
                                   repository cài đặt các port đó
```

`domain.OrderRepository` được khai báo trong `internal/domain/repository.go` và
cài đặt trong `internal/repository`. Chính phép đảo chiều này cho phép test
business rule bằng fake in-memory — xem `internal/domain/service_test.go`, chạy
dưới một giây và không cần Docker.

Những hệ quả thực tế cần giữ vững khi review:

- Không bao giờ có kiểu `gin.Context`, `pgx` hay `database/sql` xuất hiện trong
  `domain`.
- Handler chỉ decode, uỷ quyền, encode. Một câu điều kiện diễn đạt *ý nghĩa
  nghiệp vụ* thuộc về `domain`, không thuộc handler.
- SQL chỉ tồn tại trong `repository`. Lỗi từ driver được dịch sang sentinel của
  domain (`ErrNotFound`, `ErrConflict`) ngay tại ranh giới đó, không ở đâu khác.

## Kiến trúc

Đây là **Ports & Adapters** (còn gọi Hexagonal, hay nhánh thực dụng của Clean
Architecture). `domain` là phần lõi và không phụ thuộc gì ngoài stdlib cộng một
kiểu UUID; mọi thứ chạm vào thế giới bên ngoài đều là adapter cắm vào lõi đó.

| Tầng | Trách nhiệm | Được phép import | Tuyệt đối không |
|---|---|---|---|
| `domain` | entity, invariant, use case, khai báo port | stdlib, `uuid` | mọi package nội bộ khác, gin, pgx, otel |
| `repository` | adapter ra PostgreSQL, cài đặt port | `domain`, `config`, pgx | gin, otel, `net/http` |
| `transport/http` | adapter vào từ HTTP | `domain`, `config`, `observability`, gin, otel | pgx, SQL |
| `config` | đọc và validate biến môi trường | stdlib | mọi package nội bộ |
| `observability` | logger, tracer, meter | `config`, otel, slog | `domain`, `repository`, `transport` |
| `cmd/api` | composition root, ráp mọi thứ lại | tất cả | — |

Chỉ `cmd/api/main.go` được biết mặt đủ mọi kiểu cụ thể. Đó là chỗ duy nhất
`repository.NewOrderRepository(db)` gặp `domain.NewOrderService(...)`.

Bảng trên không phải nguyện vọng — nó kiểm chứng được, và nên đưa vào CI:

```bash
# domain phải sạch: lệnh này chỉ được in ra chính nó
go list -deps ./internal/domain | grep order-service

# transport không được chạm driver database
go list -deps ./internal/transport/http | grep jackc && echo "VI PHAM"
```

### Luồng một request

`POST /api/v1/orders` đi qua đúng các chặng sau:

```
1  middleware       RequestID → Tracing → Logging → Metrics → Recovery → CORS
2  order_handler    ShouldBindJSON vào createOrderRequest, parse UUID
3                   dựng domain.CreateOrderInput  ← rời khỏi thế giới HTTP tại đây
4  OrderService     sinh ID, chuẩn hoá currency, tính TotalAmount từ items
5  Order.Validate   kiểm mọi invariant, gom hết lỗi rồi mới trả về
6  OrderRepository  Create: một transaction bao cả orders lẫn order_items
7  respondError     nếu lỗi: map sentinel domain sang HTTP status
```

Điểm cần để ý là **bước 3**: từ đó trở xuống không còn kiểu nào của HTTP, và từ
bước 6 trở lên không còn kiểu nào của SQL. Hai ranh giới đó là toàn bộ giá trị
của kiến trúc này — và cũng là thứ dễ bị phá nhất khi thêm tính năng vội.

### Đổi framework hay đổi giao thức

Vì `domain` không biết gì về Gin, thay Gin bằng chi/echo/fiber chỉ cần viết lại
`internal/transport/http`. Thêm gRPC hay consumer Kafka thì tạo
`internal/transport/grpc` hoặc `internal/transport/kafka` bên cạnh, cùng gọi vào
`domain.OrderService`, không đụng một dòng nghiệp vụ nào.

## Mô hình domain

### Aggregate

`Order` là **aggregate root**, `Item` là entity con nằm trong nó.

```
Order (root)
├── ID, CustomerID          uuid.UUID
├── Status                  pending | paid | shipped | delivered | cancelled
├── Currency                mã ISO-4217, 3 ký tự
├── TotalAmount             int64, luôn được tính lại từ Items
├── CreatedAt, UpdatedAt    time.Time
└── Items []Item
    └── ID, OrderID, SKU, Name, Quantity, UnitPrice
```

`Item` **không có repository riêng**, và đó là chủ ý. Item không có vòng đời độc
lập — không ai đi sửa một dòng hàng mà không thông qua đơn chứa nó. Vì vậy toàn
bộ aggregate luôn được đọc và ghi trọn vẹn: `Create` bọc cả `orders` lẫn
`order_items` trong một transaction, và `Delete` dựa vào `ON DELETE CASCADE`.

### Invariant

`Order.Validate()` kiểm tất cả cùng lúc rồi mới trả về, thay vì dừng ở lỗi đầu
tiên, để client sửa một lần được cả payload:

| Ràng buộc | Thông báo |
|---|---|
| `CustomerID` khác `uuid.Nil` | phải là UUID không rỗng |
| `Currency` đúng 3 ký tự | mã ISO-4217 |
| `Status` là giá trị hợp lệ | nằm trong 5 trạng thái |
| có ít nhất một item | đơn không được rỗng |
| `SKU` không rỗng và **không trùng** trong cùng đơn | |
| `Quantity >= 1`, `UnitPrice >= 0` | |

Lỗi trả về là `*ValidationError`, có `Unwrap()` trả `ErrInvalidInput` — nên
`errors.Is(err, ErrInvalidInput)` vẫn đúng, đồng thời `errors.As` lấy được danh
sách field cụ thể để trả cho client.

### Máy trạng thái

Vòng đời khai báo trong `allowedTransitions`; trạng thái không có mặt trong map
là trạng thái kết thúc.

| Từ | Được phép sang |
|---|---|
| `pending` | `paid`, `cancelled` |
| `paid` | `shipped`, `cancelled` |
| `shipped` | `delivered` |
| `delivered` | *(kết thúc)* |
| `cancelled` | *(kết thúc)* |

`UpdateStatus` đọc trạng thái hiện tại trước, so với bảng trên, rồi mới ghi. Áp
lại đúng trạng thái đang có là idempotent — trả về đơn nguyên trạng, không phải
409.

### Port

Domain khai báo hai interface, cả hai đều được cài đặt ở nơi khác:

```go
type OrderRepository interface {
    Create(ctx, *Order) error
    GetByID(ctx, uuid.UUID) (*Order, error)
    List(ctx, ListFilter) (Page[Order], error)
    UpdateStatus(ctx, uuid.UUID, Status) (*Order, error)
    Delete(ctx, uuid.UUID) error
}

type HealthChecker interface {
    Health(ctx) map[string]string
}
```

`HealthChecker` tồn tại để handler health probe phụ thuộc vào domain thay vì
phụ thuộc thẳng vào kiểu `*repository.Postgres`.

`Page[T]` là generic, trả về cả `Items` lẫn `Total` chưa phân trang, để client
dựng được thanh phân trang mà không phải gọi thêm lần nữa.

### Use case

`OrderService` là nơi đặt luật bắc qua nhiều entity; luật của riêng một entity
thì nằm trên chính entity đó.

| Method | Việc nó làm ngoài gọi repository |
|---|---|
| `CreateOrder` | sinh ID, chuẩn hoá currency về chữ hoa, **tính `TotalAmount`**, validate |
| `GetOrder` | bọc lỗi kèm ngữ cảnh |
| `ListOrders` | kẹp `Limit` vào khoảng 1–100, chặn `Offset` âm, kiểm `Status` hợp lệ |
| `UpdateStatus` | enforce máy trạng thái, xử lý idempotent |
| `CancelOrder` | tên gọi tắt cho chuyển sang `cancelled` |
| `DeleteOrder` | xoá vĩnh viễn, dành cho dọn dữ liệu |

`CreateOrderInput` là kiểu **tách riêng**, không phải `Order`. Chủ ý: client
không được quyền chọn `ID`, `Status`, `CreatedAt`, và đặc biệt là
`TotalAmount` — tổng tiền luôn do server tính từ danh sách item.

Đồng hồ được tiêm qua field `now func() time.Time` thay vì gọi `time.Now()` rải
rác, nên hành vi phụ thuộc thời gian test được mà không cần thư viện giả lập.

## Database

### Đang nhắm vào PostgreSQL

Driver là **pgx/v5** dùng trực tiếp, không qua `database/sql`, với connection
pool `pgxpool`. Không dùng ORM — SQL viết tay trong `internal/repository`.

Bộ integration test chạy trên `postgres:16-alpine`. Không có tính năng nào cần
phiên bản mới hơn PostgreSQL 12.

Những chỗ **gắn chặt vào PostgreSQL**, nếu đổi DB thì phải viết lại:

| Thứ | Ở đâu |
|---|---|
| Kiểu `UUID`, `TIMESTAMPTZ`, `BIGINT` gốc | `migrations/0001_create_orders.up.sql` |
| `RETURNING` sau `UPDATE` | `order_postgres.go` — `UpdateStatus` |
| `WHERE order_id = ANY($1)` nhận mảng UUID | `order_postgres.go` — `itemsByOrderIDs` |
| Giao thức `COPY` qua `tx.CopyFrom` | `order_postgres.go` — `Create` |
| Mã lỗi SQLSTATE `23505` / `23503` | `postgres.go` — `classify` |
| Tham số đánh số `$1, $2` | mọi câu query |
| `ON DELETE CASCADE` | migration |

### Nếu muốn đổi sang DB khác

Chi phí được giới hạn đúng bằng thiết kế: `domain` và `transport` **không đổi
một dòng nào**, vì chúng chỉ biết tới interface `OrderRepository`.

| DB | Phải làm gì | Mức độ |
|---|---|---|
| MySQL / MariaDB | viết lại repository (`?` thay `$n`, bỏ `RETURNING`, `CHAR(36)` hoặc `BINARY(16)` thay `UUID`, mã lỗi `1062`, `INSERT` hàng loạt thay `COPY`) + viết lại migration | trung bình |
| SQLite | như trên, thêm việc bỏ `TIMESTAMPTZ` và tự lo concurrency | trung bình |
| MongoDB | viết lại repository theo document; `Page[T]` và `ListFilter` giữ nguyên; nhúng `Items` vào document đơn thì bỏ được transaction | lớn hơn, nhưng vẫn khu trú |
| Thêm cache Redis | không cần đổi gì — viết một implementation `OrderRepository` bọc bên ngoài cái hiện có | nhỏ |

Dòng cuối bảng là phép thử tốt nhất cho kiến trúc: thêm một tầng cache là việc
của composition root, không phải việc của nghiệp vụ.

### Quy ước schema

- **Tiền** lưu bằng `BIGINT` theo đơn vị nhỏ nhất. Không `FLOAT`, không
  `NUMERIC` — xem mục [Tiền tệ](#tiền-tệ).
- **Khoá chính** là `UUID` sinh ở phía ứng dụng, không phải `SERIAL`. Nhờ vậy
  aggregate có ID trước khi chạm database, và `Create` gán được `OrderID` cho
  từng item ngay trong bộ nhớ.
- **Thời gian** dùng `TIMESTAMPTZ`, luôn UTC.
- **Ràng buộc trong DB phản chiếu invariant của domain**, không thay thế nó:
  `CHECK` cho danh sách trạng thái, `UNIQUE (order_id, sku)` khớp với luật cấm
  SKU trùng. Domain là nơi enforce, DB là lớp phòng thủ cuối.
- **Index** đặt theo đúng truy vấn có thật:
  `(customer_id, created_at DESC)` cho listing, `(status)` cho lọc trạng thái,
  `(order_id)` trên `order_items`.

### Migration

Các cặp file đánh số trong `migrations/`, chạy bằng `golang-migrate`:

```bash
make migrate-up                        # áp dụng toàn bộ .up.sql
make migrate-down                      # lùi lại một bước
make migrate-create name=add_discount  # sinh cặp file mới
```

Integration test áp dụng chính các file này, nên schema đem ra test luôn là
schema sẽ deploy.

## Bắt đầu

```bash
cp .env.example .env      # bắt buộc có DB_DATABASE và DB_USERNAME
make docker-run           # khởi động postgres + service
make migrate-up           # áp dụng migrations/
make run                  # hoặc chạy từ source với database sẵn có
make help                 # xem toàn bộ target
```

## Test

| Lệnh | Phạm vi | Cần Docker |
|---|---|---|
| `make test` | config, domain, transport | không |
| `make test-race` | như trên, kèm race detector | không (cần CGO + trình biên dịch C) |
| `make itest` | repository trên PostgreSQL thật qua testcontainers | có |
| `make test-all` | toàn bộ | có |
| `make cover` | báo cáo coverage HTML cho các package unit test | không |

`-race` được tách thành target riêng vì nó cần CGO và một trình biên dịch C,
thứ mà bản cài Go mặc định trên Windows không có. CI nên chạy `make test-race`.

Bộ integration test áp dụng đúng các file `migrations/*.up.sql` trong repo, nên
schema đem ra test chính là schema sẽ deploy. Đặt `SKIP_DOCKER_TESTS=1` để bỏ
qua bộ này trên máy không có Docker.

## API

Các health probe nằm ở gốc vì chúng là hạ tầng, không phải API công khai có
phiên bản. Route nghiệp vụ được đánh phiên bản ngay từ đầu.

| Method | Path | Ghi chú |
|---|---|---|
| `GET` | `/livez` | process còn sống; không kiểm tra phụ thuộc nào |
| `GET` | `/readyz` | phụ thuộc còn kết nối được; trả 503 khi suy giảm |
| `GET` | `/health` | bí danh của `/readyz` |
| `POST` | `/api/v1/orders` | tạo đơn; trả 201 kèm header `Location` |
| `GET` | `/api/v1/orders` | liệt kê; lọc theo `customer_id`, `status`, `limit`, `offset` |
| `GET` | `/api/v1/orders/:id` | lấy một đơn |
| `PATCH` | `/api/v1/orders/:id/status` | chuyển trạng thái; 409 nếu vòng đời không cho phép |
| `DELETE` | `/api/v1/orders/:id` | xoá; trả 204 |

`/livez` và `/readyz` khác nhau có chủ đích. Database chết **không được** làm
restart pod — đó là vấn đề readiness, không phải liveness.

### Vòng đời đơn hàng

```
pending ──→ paid ──→ shipped ──→ delivered
   │         │
   └────→ cancelled ←┘
```

Được enforce trong `domain.Status.CanTransitionTo` và phản chiếu bằng ràng buộc
`CHECK` trong migration. Áp lại đúng trạng thái hiện tại là idempotent, không
tính là xung đột.

### Lỗi

Mọi thất bại đều trả về cùng một hình dạng, để client chỉ phải viết một bộ xử
lý lỗi duy nhất:

```json
{
  "error": {
    "code": "validation_failed",
    "message": "the request payload failed validation",
    "fields": [{ "field": "items[0].quantity", "message": "must be at least 1" }],
    "request_id": "0b9c…"
  }
}
```

| Lỗi domain | Status | Code |
|---|---|---|
| `*ValidationError` | 422 | `validation_failed` |
| `ErrNotFound` | 404 | `not_found` |
| `ErrConflict` | 409 | `conflict` |
| `ErrInvalidInput` | 400 | `invalid_input` |
| `context.Canceled` | 499 | `client_closed_request` |
| `context.DeadlineExceeded` | 504 | `timeout` |
| còn lại | 500 | `internal_error` |

Lỗi nội bộ được ghi log đầy đủ nhưng không bao giờ dội ngược ra client.
Validation báo mọi field sai cùng một lúc, thay vì mỗi lượt gọi một field.

## Observability

- **Log** — `log/slog`, mặc định JSON. Logger lấy từ context của request đã mang
  sẵn `request_id`, `trace_id` và `span_id`, nên một dòng log và một span luôn
  đối chiếu được với nhau.
- **Trace** — mỗi request một server span, đặt tên theo route template
  (`GET /api/v1/orders/:id`) để chặn cardinality. Header `traceparent` được tôn
  trọng, nên trace từ service phía trước đi tiếp xuyên qua service này.
- **Metric** — bộ RED: `http.server.request.count`,
  `http.server.request.duration`, `http.server.active_requests`.

Khi `OTEL_EXPORTER_OTLP_ENDPOINT` để trống, các provider vẫn là thật: span vẫn
được tạo và trace ID vẫn xuất hiện trong log, chỉ là không export đi đâu. Nhờ đó
môi trường dev có cùng hình dạng với production mà không cần dựng collector trên
máy cá nhân.

## Tiền tệ

Số tiền lưu bằng `int64` theo đơn vị nhỏ nhất (cent), không bao giờ dùng float,
và kiểu cột là `BIGINT`. `total_amount` luôn được tính lại từ danh sách item ở
phía server — client không thể tự quyết định đơn hàng giá bao nhiêu.

## Cấu hình

Toàn bộ nằm trong `internal/config`, được validate lúc khởi động và báo mọi lỗi
cùng lúc. Xem `.env.example` để biết danh sách đầy đủ kèm giá trị mặc định.

`DB_*` là bộ tên hiện hành. Bộ tên `BLUEPRINT_DB_*` từ scaffold gốc vẫn hoạt
động như phương án dự phòng, nên các file `.env` sẵn có và `docker-compose.yml`
trong repo vẫn chạy được; khi cả hai cùng được đặt thì `DB_*` thắng.
