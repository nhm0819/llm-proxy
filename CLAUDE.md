# CLAUDE.md — llm-proxy

OpenAI-compatible LLM API 요청을 여러 업스트림 백엔드로 라우팅하는 Go 기반 리버스 프록시.

---

## 빌드 & 테스트

```bash
# 의존성 동기화 (go.sum 없을 때)
go mod tidy

# 빌드
go build ./...

# 전체 테스트 (apikey 패키지는 Docker 필요 — 없으면 자동 skip)
go test ./...

# Docker 없는 환경: 통합 테스트 제외
go test -short ./...

# 레이스 컨디션 검사 (PR 전 필수)
go test -race ./...

# Docker 이미지
docker build -t llm-proxy:latest .

# 로컬 전체 스택 (proxy + Redis + Loki + Grafana)
docker compose up -d
```

Go 실행 파일 경로: `/c/Program\ Files/Go/bin/go` (Windows PATH에 없는 경우 직접 지정)

---

## 패키지 구조

```
cmd/proxy/main.go              진입점, 의존성 조립 (DI root)
internal/
  apikey/    store.go          PG primary + Redis cache 기반 API 키 CRUD + Resolve
             handler.go        Admin HTTP 핸들러 (/admin/keys)
             migrate.go        go:embed + RunMigrations (시작 시 자동 실행)
             store_test.go     apikey 통합 테스트 (testcontainers-go + miniredis)
             handler_test.go   Admin REST API CRUD + 인증 테스트 (Docker skip)
             migrations/
               001_create_api_keys.sql  api_keys 테이블 DDL
  config/    config.go         환경변수 로딩
             validate.go       시작 시 검증
             config_test.go    Load() 기본값·오버라이드·ROUTES_JSON 테스트
  docs/      handler.go        Swagger UI + OpenAPI spec 서빙 (/docs/)
             openapi.yaml      OpenAPI 3.0 스펙 (go:embed)
             handler_test.go   spec/UI 엔드포인트, Cache-Control 헤더 테스트
  router/    router.go         모델명 → 업스트림 라우팅 (최장 접두사 우선)
  pii/       pii.go            정규식 + 체크섬 기반 PII 탐지·마스킹
  tokencount/tokencount.go     tiktoken 토큰 카운터 (캐시 포함)
             tokencount_test.go Count 함수·캐시·fallback 테스트
  quota/     quota.go          Redis Lua: 원자적 예약(reserve) + 정산(adjust)
  ratelimit/ ratelimit.go      Redis Lua: 고정 윈도우 RPS 카운터
  audit/     audit.go          Redis Stream 감사 기록 + HashBuilder (HMAC-SHA256)
  loki/      loki.go           Loki Push API HTTP 클라이언트
             loki_test.go      Push no-op·httptest mock·라벨 머지·에러 처리 테스트
  breaker/   breaker.go        per-upstream 회로 차단기 (RoundTripper 래핑)
  metrics/   metrics.go        Prometheus Counter / Histogram / Gauge 등록
             metrics_test.go   custom registry로 메트릭 등록/기록 검증 테스트
  middleware/
    middleware.go              Recover · RequestID · AccessLog · MaxBody
    middleware_test.go         RequestID·Recover·MaxBody·Chain·AccessLog 테스트
    auth.go                    KeyResolver 인터페이스 + AuthAPIKey 미들웨어
  proxy/
    proxy.go                   핵심 ServeHTTP 핸들러
    classifier.go              경로/필드 분류·추출 헬퍼
    streamer.go                SSE 파싱 + StreamEventParser
pkg/
  httputil/  httputil.go       공유 HTTP 유틸리티 (RespondErr, CopyHeaders, NewRequestID)
             httputil_test.go  CopyHeaders·RespondErr·NewRequestID 유닛테스트
scripts/
  llm_review.py               GitLab CI LLM 코드 리뷰 스크립트 (vLLM 호출 → MR 코멘트)
```

---

## 요청 처리 흐름 (ServeHTTP 순서)

1. `MaxBody` → 본문 크기 제한
2. `RequestID` → X-Request-ID 생성/전파
3. `Recover` → 패닉 복구
4. `AccessLog` → 요청 로깅
5. `AuthAPIKey(keyStore)` → Redis 캐시 → PG에서 키 조회, X-User-ID 주입, Authorization 헤더 삭제
6. `proxy.ServeHTTP`
   - 본문 파싱 (JSON)
   - RPS 제한 확인 (Redis Lua)
   - PII 스캔 (선택적 차단)
   - 토큰 할당량 예약 (Redis Lua)
   - 업스트림 라우팅 (모델명 접두사)
   - 업스트림 HTTP 요청 (회로 차단기 Transport 적용)
   - 응답 passthrough (스트리밍 / 비스트리밍)
   - 토큰 할당량 정산 (실제 사용량으로 보정)
   - 감사 로그 + Prometheus 메트릭 + Loki (비동기 goroutine)

---

## API 키 관리

### 저장 구조

**PostgreSQL (primary)**

| 테이블 | 컬럼 | 설명 |
|--------|------|------|
| `api_keys` | key, user_id, description, created_at, expires_at | API 키 메타데이터 영구 저장 |

마이그레이션은 시작 시 `internal/apikey/migrations/001_create_api_keys.sql`을 자동 실행합니다.

**Redis (cache)**

| 키 패턴 | 타입 | 내용 |
|---------|------|------|
| `cache:apikey:<key>` | String (JSON) | user_id, description, created_at, expires_at (TTL 5분) |

- 기존 `apikey:<key>` Hash / `apikeys:index` Set 구조는 더 이상 사용하지 않음
- List() 는 PG 직접 조회 (캐시 없음)

### 캐시 전략
| 연산 | 동작 |
|------|------|
| `Resolve()` / `Get()` | Redis hit → return / miss → PG SELECT → Redis SET TTL 5m |
| `Create()` | PG INSERT → Redis SET |
| `Delete()` | Redis DEL → PG DELETE |
| `List()` | PG 직접 조회 |
| `SeedFromMap()` | PG INSERT ON CONFLICT DO NOTHING |

### 시작 시 시드 (마이그레이션)
`PROXY_API_KEYS_JSON`에 정의된 키를 시작 시 PostgreSQL에 자동으로 시드합니다.
이미 존재하는 키는 덮어쓰지 않습니다 (멱등성).

### KeyResolver 인터페이스
```go
// middleware.KeyResolver
type KeyResolver interface {
    Resolve(ctx context.Context, key string) (userID string, found bool, err error)
}
```
`apikey.Store`가 구현합니다. 테스트에서는 `middleware.StaticKeyRegistry`를 직접 사용하세요.

### Admin REST API (`/admin/keys`)
`ADMIN_API_KEY` 환경변수를 Bearer 토큰으로 인증합니다. 미설정 시 503 반환.

```bash
# 키 생성
curl -X POST http://localhost:8080/admin/keys \
  -H "Authorization: Bearer $ADMIN_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"user_id": "alice", "description": "Alice 서비스용"}'
# → {"key":"sk-proxy-...","user_id":"alice","created_at":"..."}

# 만료 시간 지정 (RFC3339)
  -d '{"user_id": "bob", "expires_at": "2026-12-31T00:00:00Z"}'

# 전체 키 목록
curl http://localhost:8080/admin/keys \
  -H "Authorization: Bearer $ADMIN_API_KEY"

# 키 폐기 (204 No Content)
curl -X DELETE http://localhost:8080/admin/keys/sk-proxy-xxx \
  -H "Authorization: Bearer $ADMIN_API_KEY"
```

| Method | Path | 설명 |
|--------|------|------|
| `POST` | `/admin/keys` | 새 키 생성 |
| `GET` | `/admin/keys` | 전체 키 목록 |
| `GET` | `/admin/keys/{key}` | 키 메타데이터 조회 |
| `DELETE` | `/admin/keys/{key}` | 키 폐기 |

---

## API 문서 (Swagger UI)

`internal/docs/openapi.yaml`을 `//go:embed`로 내장. 외부 의존성 없음.

| 경로 | 내용 |
|------|------|
| `GET /docs/` | Swagger UI (CDN 로드) |
| `GET /docs/openapi.yaml` | OpenAPI 3.0 raw spec |

스펙 수정 시 `internal/docs/openapi.yaml`만 편집. 재빌드하면 바이너리에 반영됨.

---

## 핵심 설계 원칙

### Redis Lua 스크립트
`quota.go`, `ratelimit.go` 모두 Lua 스크립트로 원자적 연산. 스크립트 수정 시 EVALSHA 캐시를 고려해 `redis.NewScript()`로 등록.

### 토큰 할당량 헤더
비스트리밍: 토큰 계산 → Adjust → `setQuotaHeaders` → `WriteHeader` → `Write` 순서를 반드시 유지. `WriteHeader` 이후 헤더 설정 불가.

### SSE 스트리밍
`middleware.responseWriter`는 `http.Flusher`를 위임 구현. 미구현 시 SSE 청크가 클라이언트에 즉시 전달되지 않음.

### 회로 차단기
`breaker.Breaker`는 `http.RoundTripper`로 구현되어 `HTTPClient.Transport`에 삽입. 업스트림 호스트별로 독립 circuit 유지.

### 인증
`AuthAPIKey`는 `KeyResolver` 인터페이스를 받습니다 — 프로덕션에서는 `apikey.Store` (PG primary + Redis cache), 테스트에서는 `StaticKeyRegistry`. 시작 시 `PROXY_API_KEYS_JSON`을 PostgreSQL에 시드 후 PG가 단일 소스.

---

## 테스트 가이드

현재 커버리지: **73.4%** (54.5% → +18.9pp, 2026-03-20 기준)

| 패키지 | 테스트 방식 |
|--------|------------|
| `apikey` | testcontainers-go (실제 PostgreSQL 컨테이너) + miniredis; Docker 미실행 시 자동 skip (`-short` 플래그 또는 Docker 데몬 없을 때) |
| `apikey` (handler) | Admin REST API CRUD + 인증 + disabled + method not allowed (12개, Docker skip) |
| `audit` | miniredis + redis 클라이언트(`rdb.XRange`, `rdb.HGetAll`)로 검증 — miniredis 직접 메서드 사용 금지 |
| `quota`, `ratelimit` | miniredis + redis 클라이언트; concurrent Reserve·독립 키·TTL 만료·limit=1 엣지 케이스 포함 |
| `proxy` (통합) | `httptest.Server` fake upstream + miniredis; `StaticKeyRegistry` 사용 |
| `breaker`, `pii`, `router` | 순수 단위 테스트; per-host 독립 circuit·probe fail reopens·State String 포함 |
| `config` | Load() 기본값, 환경변수 오버라이드, ROUTES_JSON 파싱 (7개) |
| `middleware` | RequestID, Recover, MaxBody, Chain, AccessLog (9개) |
| `httputil` | CopyHeaders, RespondErr, NewRequestID (7개) |
| `tokencount` | Count 함수, 캐시, fallback (6개) |
| `loki` | Push no-op, httptest mock, 라벨 머지, 에러 처리 (5개) |
| `metrics` | custom registry로 메트릭 등록/기록 검증 (7개) |
| `docs` | spec/UI 엔드포인트, Cache-Control 헤더 (3개) |

**중요**: miniredis v2.33.0은 `XRange`, `HGetAll` 직접 메서드 없음. `TTL`은 `time.Duration` 단일 반환.

---

## 개발 환경 (VS Code)

`.vscode/launch.json` 에 디버그/테스트 구성 포함. 처음 사용 시:

```bash
cp .env.example .env   # 실제 값 입력 후 사용
```

주요 launch 구성:
- **`Proxy: Run`** — `.env` 기반 서버 기동
- **`Proxy: Run (dry)`** — upstream 없이 설정만 확인 (Redis 로컬 필요)
- **`Test: All packages (-race)`** — 전체 테스트
- **`Test: Current package`** — 현재 열린 파일의 패키지만
- **`Test: Single function (input)`** — 함수명 입력 → 해당 테스트만

---

## CI/CD (GitLab)

브랜치 전략: `BRANCHING.md` 참조.

| 태그 패턴 | 트리거 | 결과 |
|-----------|--------|------|
| `v0.1.0-rc1` | `dev` 브랜치 | dev 환경 자동 배포 |
| `v0.1.0` | `main` 브랜치 | production 수동 승인 후 배포 |

### LLM 코드 리뷰 (MR 자동)
MR 오픈 시 `scripts/llm_review.py`가 실행되어 vLLM에 diff를 보내고 결과를 MR 코멘트로 등록.

**diff 필터링 전략** (`scripts/llm_review.py`):
- 생성 코드 제외: `vendor/`, `*.pb.go`, `mock_*.go`, `*_gen.go`, `zz_generated*.go`
- `-U15` context 확대 (기본 3줄 → 15줄)
- 파일 우선순위 정렬: non-trivial → trivial (주석·공백·import만 변경)
- 파일 단위 budget fill (중간 잘림 방지, 최대 15,000줄)

**모델 설정** (Qwen3.5-122B-A10B 기준, 262K context):

| 파라미터 | 값 | 근거 |
|----------|----|------|
| `LLM_MAX_DIFF_LINES` | 15,000 | 262K ctx − 프롬프트/응답/버퍼 ≈ 240K토큰 ÷ 16tok/줄 |
| `max_tokens` | 8,192 | 대형 MR 전체 분석 여유 |
| `temperature` | 0.6 | thinking mode가 내부 추론 담당 |
| `LLM_THINKING` | true | `reasoning_content` → MR `<details>` 접기 |
| CI timeout | 15분 | thinking 추론 포함 |

필요 CI/CD Variables: `LLM_API_BASE_URL`, `LLM_API_KEY`, `LLM_MODEL`, `GITLAB_TOKEN`

---

## 주요 환경변수

| 변수 | 기본값 | 설명 |
|------|--------|------|
| `UPSTREAM_BASE_URL` | `https://api.openai.com` | 기본 업스트림 |
| `UPSTREAM_API_KEY` | — | 업스트림 API 키 |
| `ROUTES_JSON` | — | 멀티 업스트림 라우팅 규칙 (JSON 배열) |
| `PROXY_API_KEYS_JSON` | — | 시작 시 Redis에 시드할 정적 키 맵 |
| `ADMIN_API_KEY` | — | Admin API Bearer 토큰 (미설정 시 비활성) |
| `DATABASE_URL` | — | PostgreSQL DSN (`postgres://user:pass@host:5432/db?sslmode=disable`) |
| `REDIS_ADDR` | `localhost:6379` | Redis 주소 |
| `TOKEN_DAILY_LIMIT` | `200000` | 사용자별 일일 토큰 한도 |
| `TOKEN_SAFETY_FACTOR` | `1.20` | 토큰 예약 안전 계수 (≥ 1.0) |
| `RATE_LIMIT_RPS` | `5` | 사용자별 초당 요청 수 |
| `PII_BLOCK` | `false` | PII 탐지 시 차단 여부 |
| `QUOTA_TIMEZONE` | `Asia/Seoul` | 일일 리셋 기준 시간대 |
| `AUDIT_HMAC_KEY` | — | 감사 해시 HMAC 키 (미설정=SHA-256) |
| `LOKI_PUSH_URL` | — | Loki 엔드포인트 (미설정=비활성) |

전체 목록: [ENV.md](./ENV.md) | 로컬 개발용 템플릿: [.env.example](../.env.example)

---

## 엔드포인트 & 포트

| 서비스 | 포트 | 주요 경로 |
|--------|------|-----------|
| llm-proxy | `8080` | `/v1/*`, `/healthz`, `/metrics`, `/admin/keys`, `/docs/` |
| PostgreSQL | `5432` | — |
| Redis | `6379` | — |
| Loki | `3100` | — |
| Grafana | `3000` | admin/admin |
