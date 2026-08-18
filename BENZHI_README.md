# task109-loanamort

A loan amortization and repayment engine: create amortizing loans (等额本息 /
等额本金 / 到期还本付息), generate exact-to-the-cent schedules, record scheduled
payments, prepay with term-shortening or payment-lowering recasts, refinance via
rate changes, and query as-of outstanding balances recomputed from the
persistent payment ledger. All money is integer cents; all state is persisted
to SQLite and survives restart.

## What it solves

Lenders need a service that turns a loan's principal/rate/term into a
per-period amortization schedule whose principal sums back to the original
principal exactly (tail rounding correction), accepts payments that match the
plan, supports extra-principal prepayments that recast the remaining schedule
(reduce_term keeps the payment and shortens; reduce_payment keeps the term and
lowers the payment), supports mid-term rate changes that recompute the payment,
and always reports an outstanding balance that is a pure function of the
payment ledger — never a cached running balance — so restarts recover exactly.

## Main inputs / outputs

- Inputs: principal (integer cents), annual rate (percent), periods, loan type,
  payment amounts (cents), prepayment amounts + strategy, new annual rate.
- Outputs: amortization schedules, outstanding balances (as-of), payoffs,
  summaries, projections, payment ledger, portfolio stats, consistency reports.

## Local commands

```bash
go build ./...       # 编译
go run .             # 启动 HTTP 服务 (默认 :8080, SQLite loans.db)
go run . --smoke-test # 自检（不依赖外部服务、不睡眠）
go test ./...         # 单元/集成测试
```

环境变量 `LOAN_ADMIN_TOKEN` 覆盖管理端点（POST /admin/recompute）的共享密钥，
默认 `admin-secret`。`--db <path>` 指定 SQLite 文件，默认 `loans.db`。

## HTTP API (主要)

- `POST /borrowers` · `GET /borrowers` · `GET /borrowers/{id}` · `GET /borrowers/{id}/loans`
- `POST /loans` · `GET /loans` · `GET /loans/{id}` · `PATCH /loans/{id}`
- `GET /loans/{id}/schedule` · `GET /loans/{id}/balance?as_of=N` · `GET /loans/{id}/payoff?as_of=N`
- `GET /loans/{id}/summary` · `GET /loans/{id}/projection?periods=N` · `GET /loans/{id}/accrued-interest?as_of=N`
- `POST /loans/{id}/payments` · `POST /loans/{id}/prepayments` · `POST /loans/{id}/rate-changes`
- `GET /loans/{id}/payments` · `GET /loans/{id}/payments/{pid}` · `GET /payments`
- `GET /stats` · `POST /admin/recompute` · `GET /healthz` · `GET /version`

## Docker

构建脚本的两个参数：镜像名、平台。

```bash
bash ./build_benzhi_docker.sh go-task-benzhi:amd64 linux/amd64   # amd64
bash ./build_benzhi_docker.sh go-task-benzhi:arm64 linux/arm64   # arm64
docker run -it go-task-benzhi:amd64        # 进入容器
```

多架构镜像（本机 Go 1.26.3 工具链，CGO_ENABLED=0）：

```bash
docker buildx build --platform linux/amd64  --load -t task109-loanamort:amd64  -f Dockerfile .
docker buildx build --platform linux/arm64  --load -t task109-loanamort:arm64  -f Dockerfile .
docker run --rm task109-loanamort:amd64  --smoke-test
docker run --rm task109-loanamort:arm64  --smoke-test
```
