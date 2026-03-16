# CLAUDE.md — llm-proxy

OpenAI-compatible LLM API 요청을 여러 업스트림 백엔드로 라우팅하는 Go 기반 리버스 프록시.

---

## 빌드 & 테스트

```bash
# 의존성 동기화 (go.sum 없을 때)
go mod tidy

# 빌드
go build ./...

# 전체 테스트 (miniredis 사용 — 실제 Redis 불필요)
go test ./...

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
  config/    config.go         환경변수 로딩
             validate.go       시작 시 검증
  router/    router.go         모델명 접두사 → 업스트림 RouteRule (최장 접두사 우선)
  pii/       pii.go            정규식 + 체크섬 기반 PII 탐지·마스킹
  tokencount/tokencount.go     tiktoken 토큰 카운터 (캐시 포함)
  quota/     quota.go          Redis Lua: 원자적 예약(reserve) + 정산(adjust)
  ratelimit/ ratelimit.go      Redis Lua: 고정 윈도우 RPS 카운터
  audit/     audit.go          Redis Stream 감사 기록 + HashBuilder (HMAC-SHA256)
  loki/      loki.go           Loki Push API HTTP 클라이언트
  breaker/   breaker.go        per-upstream 회로 차단기 (RoundTripper 래핑)
  metrics/   metrics.go        Prometheus Counter / Histogram / Gauge 등록
  middleware/
    middleware.go              Recover · RequestID · AccessLog · MaxBody
    auth.go                    프록시 API 키 인증 → X-User-ID 주입
  proxy/
    proxy.go                   핵심 ServeHTTP 핸들러
    classifier.go              경로/필드 분류·추출 헬퍼
    streamer.go                SSE 파싱 + StreamEventParser
pkg/
  httputil/  httputil.go       공유 HTTP 유틸리티 (RespondErr, CopyHeaders, NewRequestID)
```

---

## 요청 처리 흐름 (ServeHTTP 순서)

1. `MaxBody` → 본문 크기 제한
2. `RequestID` → X-Request-ID 생성/전파
3. `Recover` → 패닉 복구
4. `AccessLog` → 요청 로깅
5. `AuthAPIKey` → 프록시 키 검증, X-User-ID 주입, Authorization 헤더 삭제
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

## 핵심 설계 원칙

### Redis Lua 스크립트
`quota.go`, `ratelimit.go` 모두 Lua 스크립트로 원자적 연산. 스크립트 수정 시 EVALSHA 캐시를 고려해 `redis.NewScript()`로 등록.

### 토큰 할당량 헤더
비스트리밍: 토큰 계산 → Adjust → `setQuotaHeaders` → `WriteHeader` → `Write` 순서를 반드시 유지. `WriteHeader` 이후 헤더 설정 불가.

### SSE 스트리밍
`middleware.responseWriter`는 `http.Flusher`를 위임 구현해야 함. 미구현 시 SSE 청크가 클라이언트에 즉시 전달되지 않음.

### 회로 차단기
`breaker.Breaker`는 `http.RoundTripper`로 구현되어 `HTTPClient.Transport`에 삽입. 업스트림 호스트별로 독립 circuit 유지.

### 인증
`PROXY_API_KEYS_JSON`이 빈 값이면 인증 스킵 (개발 모드). 프로덕션에서는 반드시 설정. 업스트림 API 키는 라우트 설정에만 존재하며 클라이언트에 노출되지 않음.

---

## 테스트 가이드

| 패키지 | 테스트 방식 |
|--------|------------|
| `audit` | miniredis + redis 클라이언트(`rdb.XRange`, `rdb.HGetAll`)로 검증 — miniredis 직접 메서드 사용 금지 |
| `quota`, `ratelimit` | miniredis + redis 클라이언트 |
| `proxy` (통합) | `httptest.Server` fake upstream + miniredis |
| `breaker`, `pii`, `router` | 순수 단위 테스트 |

**중요**: miniredis v2.33.0은 `XRange`, `HGetAll` 직접 메서드가 없고 `TTL`은 `time.Duration` 단일 반환. redis 클라이언트를 통해 검증할 것.

---

## 주요 환경변수

| 변수 | 기본값 | 설명 |
|------|--------|------|
| `UPSTREAM_BASE_URL` | `https://api.openai.com` | 기본 업스트림 |
| `UPSTREAM_API_KEY` | — | 업스트림 API 키 |
| `ROUTES_JSON` | — | 멀티 업스트림 라우팅 규칙 (JSON 배열) |
| `PROXY_API_KEYS_JSON` | — | 프록시 키 → userID 맵 (빈값=인증없음) |
| `REDIS_ADDR` | `localhost:6379` | Redis 주소 |
| `TOKEN_DAILY_LIMIT` | `200000` | 사용자별 일일 토큰 한도 |
| `TOKEN_SAFETY_FACTOR` | `1.20` | 토큰 예약 안전 계수 (≥ 1.0) |
| `RATE_LIMIT_RPS` | `5` | 사용자별 초당 요청 수 |
| `PII_BLOCK` | `false` | PII 탐지 시 차단 여부 |
| `QUOTA_TIMEZONE` | `Asia/Seoul` | 일일 리셋 기준 시간대 |
| `AUDIT_HMAC_KEY` | — | 감사 해시 HMAC 키 (미설정=SHA-256) |
| `LOKI_PUSH_URL` | — | Loki 엔드포인트 (미설정=비활성) |

전체 목록: [ENV.md](./ENV.md)

---

## 포트

| 서비스 | 포트 |
|--------|------|
| llm-proxy | `8080` (`/healthz`, `/metrics` 포함) |
| Redis | `6379` |
| Loki | `3100` |
| Grafana | `3000` (admin/admin) |
