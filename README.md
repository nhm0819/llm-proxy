# llm-proxy

OpenAI-compatible LLM API 요청을 여러 업스트림 백엔드로 라우팅하는 Go 기반 리버스 프록시입니다.
토큰 할당량 관리, 사용자별 RPS 제한, PII 탐지·차단, 감사 로그, Prometheus 메트릭, 회로 차단기를 기본으로 제공합니다.

```
클라이언트  →  llm-proxy  →  OpenAI / Anthropic / Azure / ...
                 │
                 ├─ 인증 (프록시 자체 API 키)
                 ├─ RPS 제한 (Redis, 사용자별)
                 ├─ PII 탐지 · 차단
                 ├─ 토큰 할당량 (Redis, 일일)
                 ├─ 모델명 → 업스트림 라우팅
                 ├─ 회로 차단기
                 ├─ 감사 로그 (Redis Stream + HMAC)
                 └─ Prometheus 메트릭 / Loki 로그
```

---

## 목차

1. [기능 요약](#기능-요약)
2. [아키텍처](#아키텍처)
3. [빠른 시작](#빠른-시작)
4. [로컬 개발 환경 (Docker)](#로컬-개발-환경-docker)
5. [Kubernetes 배포](#kubernetes-배포)
6. [설정](#설정)
7. [API 사용법](#api-사용법)
8. [모델 라우팅](#모델-라우팅)
9. [인증](#인증)
10. [토큰 할당량](#토큰-할당량)
11. [RPS 제한](#rps-제한)
12. [PII 탐지](#pii-탐지)
13. [감사 로그](#감사-로그)
14. [관찰 가능성](#관찰-가능성)
15. [회로 차단기](#회로-차단기)
16. [개발 & 테스트](#개발--테스트)
17. [패키지 구조](#패키지-구조)

---

## 기능 요약

| 기능 | 설명 |
|------|------|
| **다중 업스트림 라우팅** | 모델명 접두사로 OpenAI·Anthropic·Azure 등 분기 |
| **프록시 API 키 인증** | 자체 키 발급 → 업스트림 키 은닉 |
| **일일 토큰 할당량** | Redis Lua 스크립트로 원자적 예약·정산 |
| **RPS 제한** | 사용자별 초당 요청 수 제한 (고정 윈도우) |
| **PII 탐지·차단** | 한국 주민등록번호·이메일·휴대폰 탐지, 선택적 차단 |
| **회로 차단기** | 업스트림 장애 감지 → 자동 fail-fast + 복구 |
| **감사 로그** | Redis Stream에 요청·응답 해시(HMAC-SHA256) 기록 |
| **Prometheus 메트릭** | 7종 지표 `/metrics` 노출 |
| **Loki 로그 전송** | 구조화된 JSON 로그를 Loki Push API로 전송 |
| **스트리밍 지원** | SSE(Server-Sent Events) 응답 passthrough + 이벤트 파싱 |

---

## 아키텍처

```
cmd/proxy/main.go                  진입점, 의존성 조립 (DI root)
internal/
  apikey/          store.go        PG primary + Redis cache 기반 API 키 CRUD + Resolve
                   handler.go      Admin HTTP 핸들러 (/admin/keys)
                   migrate.go      go:embed + RunMigrations (시작 시 자동 실행)
                   store_test.go   apikey 통합 테스트 (testcontainers-go + miniredis)
                   migrations/001_create_api_keys.sql
  config/          config.go       환경변수 로딩 + 시작 시 검증
                   validate.go
  router/          router.go       모델명 접두사 → 업스트림 RouteRule 선택
  pii/             pii.go          정규식 기반 PII 탐지 & 마스킹
  tokencount/      tokencount.go   tiktoken 토큰 카운터 (캐시 포함)
  quota/           quota.go        Redis Lua: 원자적 예약(reserve) + 정산(adjust)
  ratelimit/       ratelimit.go    Redis Lua: 고정 윈도우 RPS 카운터
  audit/           audit.go        Redis Stream 감사 기록 + HashBuilder
  loki/            loki.go         Loki Push API HTTP 클라이언트
  breaker/         breaker.go      per-upstream 회로 차단기 (RoundTripper 래핑)
  metrics/         metrics.go      Prometheus Counter / Histogram / Gauge 등록
  middleware/
    middleware.go                  Recover · RequestID · AccessLog · MaxBody
    auth.go                        프록시 API 키 인증
  proxy/
    proxy.go                       핵심 ServeHTTP 핸들러
    classifier.go                  경로/필드 분류·추출 헬퍼
    streamer.go                    SSE 파싱 + StreamEventParser
pkg/
  httputil/        httputil.go     공유 HTTP 유틸리티
```

### 요청 처리 흐름

```
HTTP 요청
  │
  ▼
[MaxBody] 요청 본문 크기 제한
  │
  ▼
[RequestID] X-Request-ID 생성 또는 전파
  │
  ▼
[Recover] 패닉 복구 → HTTP 500
  │
  ▼
[AuthAPIKey] 프록시 API 키 검증 → X-User-ID 주입
  │
  ▼
[proxy.ServeHTTP]
  ├─ 본문 파싱 (JSON)
  ├─ RPS 제한 확인 (Redis)
  ├─ PII 스캔 (선택적 차단)
  ├─ 토큰 할당량 예약 (Redis)
  ├─ 업스트림 라우팅 (모델명 접두사)
  ├─ 업스트림 HTTP 요청 (회로 차단기 적용)
  ├─ 응답 passthrough (스트리밍 / 비스트리밍)
  ├─ 토큰 할당량 정산 (실제 사용량으로 보정)
  └─ 감사 로그 + Prometheus 메트릭 + Loki (비동기)
```

---

## 빠른 시작

### Docker Compose (권장)

```bash
git clone https://github.com/nhm0819/llm-proxy.git
cd llm-proxy

# 환경변수 설정
export OPENAI_API_KEY=sk-...

# 실행 (proxy + PostgreSQL + Redis + Loki + Grafana + Alloy + Prometheus)
docker compose up -d

# 동작 확인
curl http://localhost:8080/healthz
```

### 바이너리 직접 실행

```bash
go build -o llm-proxy ./cmd/proxy

UPSTREAM_API_KEY=sk-... \
REDIS_ADDR=localhost:6379 \
./llm-proxy
```

### 첫 번째 요청

```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer sk-proxy-your-key" \
  -d '{
    "model": "gpt-4o",
    "messages": [{"role": "user", "content": "hello"}]
  }'
```

응답 헤더에 토큰 사용량이 포함됩니다.

```
X-Token-Limit: 200000
X-Token-Used: 312
X-Token-Remaining: 199688
X-Request-ID: 3f2a1b...
```

---

## 로컬 개발 환경 (Docker)

로컬에서 프록시 전체 스택(프록시 + PostgreSQL + Redis + Loki + Grafana + Alloy + Prometheus)을 한 번에 띄울 수 있습니다.

### 1. 사전 준비

```bash
git clone https://github.com/nhm0819/llm-proxy
cd llm-proxy

# 환경 파일 생성
cp .env.example .env
```

`.env` 파일을 열고 최소 두 값을 채웁니다.

```dotenv
# .env
OPENAI_API_KEY=sk-...                        # 실제 OpenAI API 키
PROXY_API_KEYS_JSON={"sk-proxy-dev":"dev"}   # 로컬 테스트용 프록시 키
```

### 2. 스택 실행

```bash
docker compose up -d
```

컨테이너 상태 확인:

```
CONTAINER                STATUS
llm-proxy-proxy-1         Up  (healthy)
llm-proxy-postgres-1      Up  (healthy)
llm-proxy-redis-1         Up  (healthy)
llm-proxy-loki-1          Up
llm-proxy-grafana-1       Up
llm-proxy-alloy-1         Up
llm-proxy-prometheus-1    Up
```

### 3. 동작 확인

```bash
# 헬스체크
curl http://localhost:8080/healthz
# → ok

# Prometheus 메트릭
curl -s http://localhost:8080/metrics | grep llm-proxy_requests

# 첫 번째 API 호출
curl http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer sk-proxy-dev" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4o-mini",
    "messages": [{"role": "user", "content": "ping"}]
  }'
```

### 4. Grafana 확인

브라우저에서 `http://localhost:3000` 접속 (admin / admin).

Prometheus와 Loki 데이터소스는 `config/grafana/provisioning/datasources/datasources.yml`로 자동 등록됩니다. 별도 설정 없이 바로 사용 가능합니다.

- **Explore → Loki** 탭에서 쿼리: `{app="llm-proxy"}`
- **Explore → Prometheus** 탭에서 쿼리: `llm_proxy_requests_total`
- Prometheus UI는 `http://localhost:9090`에서 직접 조회 가능

### 5. Redis 감사 로그 확인

```bash
# 컨테이너 내 redis-cli 접속
docker compose exec redis redis-cli

# 최근 요청 5건 확인
XREVRANGE audit:llm-proxy + - COUNT 5

# 특정 요청 상세 (request_id로 조회)
HGETALL audit:req:<request-id>
```

### 6. 개별 서비스 재시작 / 로그

```bash
# 프록시 로그 실시간 확인
docker compose logs -f proxy

# 코드 수정 후 프록시만 재빌드
docker compose up -d --build proxy

# 전체 종료 (볼륨 보존)
docker compose down

# 전체 종료 + 볼륨 삭제 (완전 초기화)
docker compose down -v
```

### 서비스 포트 요약

| 서비스 | 로컬 포트 | 설명 |
|--------|-----------|------|
| llm-proxy | `8080` | 프록시 API + `/healthz` + `/metrics` |
| PostgreSQL | `5432` | API 키 primary 저장소 |
| Redis | `6379` | 캐시 + 할당량·RPS·감사 (직접 접근 필요 시) |
| Loki | `3100` | 로그 수집 엔드포인트 |
| Grafana | `3000` | 대시보드 (`admin` / `admin`) — datasource 자동 provisioning |
| Grafana Alloy | `12345` | 메트릭 수집 에이전트 (proxy scrape → Prometheus, Docker 로그 → Loki) |
| Prometheus | `9090` | 메트릭 저장소 (`/graph` UI 포함) |


---

## Kubernetes 배포

로컬 Docker 환경에서 동작을 확인한 뒤 Kubernetes에 프로덕션 배포합니다.
아래 매니페스트는 `deploy/k8s/` 디렉터리에 두고 관리하는 것을 권장합니다.

### 사전 요구사항

| 항목 | 버전 | 용도 |
|------|------|------|
| kubectl | 1.28+ | 클러스터 조작 |
| 컨테이너 레지스트리 | GHCR / ECR / GCR 등 | 빌드한 이미지 저장 |
| Redis | 7+ (Sentinel 또는 Cluster 권장) | 할당량·속도 제한 상태 저장 |

### 이미지 빌드 & 푸시

```bash
IMAGE=ghcr.io/nhm0819/llm-proxy
# latest 대신 Git SHA로 고정 — 프로덕션 필수
TAG=$(git rev-parse --short HEAD)

docker build -t $IMAGE:$TAG .
docker push $IMAGE:$TAG

echo "배포 이미지: $IMAGE:$TAG"
```

### 디렉터리 구조

```
deploy/k8s/
├── namespace.yaml
├── secret.yaml          # API 키 (Sealed Secrets 또는 External Secrets 권장)
├── configmap.yaml       # 비밀이 아닌 설정값
├── deployment.yaml
├── service.yaml
├── hpa.yaml             # HorizontalPodAutoscaler
├── pdb.yaml             # PodDisruptionBudget
└── servicemonitor.yaml  # Prometheus Operator용 (선택)
```

### 1. Namespace

```yaml
# deploy/k8s/namespace.yaml
apiVersion: v1
kind: Namespace
metadata:
  name: llm-proxy
```

### 2. Secret

업스트림 API 키와 프록시 API 키를 Secret으로 관리합니다.
프로덕션에서는 [Sealed Secrets](https://github.com/bitnami-labs/sealed-secrets) 또는
[External Secrets Operator](https://external-secrets.io/)를 강력히 권장합니다.

```yaml
# deploy/k8s/secret.yaml
apiVersion: v1
kind: Secret
metadata:
  name: llm-proxy-secrets
  namespace: llm-proxy
type: Opaque
stringData:
  UPSTREAM_API_KEY: "sk-..."          # 업스트림 실제 API 키
  DATABASE_URL: "postgres://llm_proxy:password@postgres.db.svc.cluster.local:5432/llm_proxy?sslmode=require"
  PROXY_API_KEYS_JSON: |
    {
      "sk-proxy-service-a": "service-a",
      "sk-proxy-service-b": "service-b"
    }
  REDIS_PASSWORD: "your-redis-password"
  AUDIT_HMAC_KEY: "your-hmac-secret-32bytes+"
```

### 3. ConfigMap

```yaml
# deploy/k8s/configmap.yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: llm-proxy-config
  namespace: llm-proxy
data:
  UPSTREAM_BASE_URL: "https://api.openai.com"
  REDIS_ADDR: "redis-master.redis.svc.cluster.local:6379"
  TOKEN_DAILY_LIMIT: "500000"
  TOKEN_SAFETY_FACTOR: "1.20"
  QUOTA_TIMEZONE: "Asia/Seoul"
  RATE_LIMIT_RPS: "10"
  PII_BLOCK: "false"
  LOKI_PUSH_URL: "http://loki-gateway.monitoring.svc.cluster.local/loki/api/v1/push"
  LOKI_LABELS: "app=llm-proxy,env=production"
  AUDIT_STREAM_KEY: "audit:llm-proxy"
  AUDIT_RECORD_TTL_SECONDS: "604800"
  AUDIT_STORE_REQUEST_RECORD: "true"
  UPSTREAM_TIMEOUT_SECONDS: "300"
```

### 4. Deployment

```yaml
# deploy/k8s/deployment.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: llm-proxy
  namespace: llm-proxy
  labels:
    app: llm-proxy
spec:
  replicas: 2
  selector:
    matchLabels:
      app: llm-proxy
  strategy:
    type: RollingUpdate
    rollingUpdate:
      maxSurge: 1
      maxUnavailable: 0       # 무중단 배포
  template:
    metadata:
      labels:
        app: llm-proxy
      annotations:
        prometheus.io/scrape: "true"
        prometheus.io/port: "8080"
        prometheus.io/path: "/metrics"
    spec:
      terminationGracePeriodSeconds: 60   # 진행 중인 스트리밍 처리 여유
      containers:
        - name: llm-proxy
          image: ghcr.io/nhm0819/llm-proxy:latest
          ports:
            - name: http
              containerPort: 8080
          envFrom:
            - configMapRef:
                name: llm-proxy-config
            - secretRef:
                name: llm-proxy-secrets
          livenessProbe:
            httpGet:
              path: /healthz
              port: 8080
            initialDelaySeconds: 5
            periodSeconds: 10
            failureThreshold: 3
          readinessProbe:
            httpGet:
              path: /healthz
              port: 8080
            initialDelaySeconds: 3
            periodSeconds: 5
            failureThreshold: 2
          resources:
            requests:
              cpu: "100m"
              memory: "128Mi"
            limits:
              cpu: "500m"
              memory: "256Mi"
          securityContext:
            readOnlyRootFilesystem: true
            runAsNonRoot: true
            runAsUser: 65532         # distroless nonroot
            allowPrivilegeEscalation: false
            capabilities:
              drop: ["ALL"]
      affinity:
        podAntiAffinity:
          preferredDuringSchedulingIgnoredDuringExecution:
            - weight: 100
              podAffinityTerm:
                labelSelector:
                  matchLabels:
                    app: llm-proxy
                topologyKey: kubernetes.io/hostname   # 노드 분산
```

### 5. Service

```yaml
# deploy/k8s/service.yaml
apiVersion: v1
kind: Service
metadata:
  name: llm-proxy
  namespace: llm-proxy
  labels:
    app: llm-proxy
spec:
  selector:
    app: llm-proxy
  ports:
    - name: http
      port: 80
      targetPort: 8080
  type: ClusterIP
```

클러스터 외부에서 접근하려면 Ingress 또는 LoadBalancer로 노출합니다.

```yaml
# Ingress 예시 (nginx-ingress)
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: llm-proxy
  namespace: llm-proxy
  annotations:
    nginx.ingress.kubernetes.io/proxy-read-timeout: "300"    # 스트리밍 대응
    nginx.ingress.kubernetes.io/proxy-send-timeout: "300"
    nginx.ingress.kubernetes.io/proxy-buffering: "off"       # SSE passthrough
spec:
  ingressClassName: nginx
  rules:
    - host: llm-proxy.example.com
      http:
        paths:
          - path: /
            pathType: Prefix
            backend:
              service:
                name: llm-proxy
                port:
                  name: http
  tls:
    - hosts:
        - llm-proxy.example.com
      secretName: llm-proxy-tls
```

### 6. HPA (HorizontalPodAutoscaler)

```yaml
# deploy/k8s/hpa.yaml
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: llm-proxy
  namespace: llm-proxy
spec:
  scaleTargetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: llm-proxy
  minReplicas: 2
  maxReplicas: 10
  metrics:
    - type: Resource
      resource:
        name: cpu
        target:
          type: Utilization
          averageUtilization: 60
    - type: Resource
      resource:
        name: memory
        target:
          type: Utilization
          averageUtilization: 70
```

### 7. PDB (PodDisruptionBudget)

노드 드레인·업그레이드 중에도 최소 1개 Pod이 유지됩니다.

```yaml
# deploy/k8s/pdb.yaml
apiVersion: policy/v1
kind: PodDisruptionBudget
metadata:
  name: llm-proxy
  namespace: llm-proxy
spec:
  minAvailable: 1
  selector:
    matchLabels:
      app: llm-proxy
```

### 8. ServiceMonitor (Prometheus Operator)

```yaml
# deploy/k8s/servicemonitor.yaml
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: llm-proxy
  namespace: llm-proxy
  labels:
    release: kube-prometheus-stack    # Helm 릴리스명에 맞게 조정
spec:
  selector:
    matchLabels:
      app: llm-proxy
  endpoints:
    - port: http
      path: /metrics
      interval: 15s
```

### 9. 단계별 배포

```bash
# ① 네임스페이스 먼저 생성
kubectl apply -f deploy/k8s/namespace.yaml

# ② Secret → ConfigMap → Deployment 순서로 적용
kubectl apply -f deploy/k8s/secret.yaml
kubectl apply -f deploy/k8s/configmap.yaml
kubectl apply -f deploy/k8s/deployment.yaml
kubectl apply -f deploy/k8s/service.yaml

# ③ HPA / PDB / ServiceMonitor (선택)
kubectl apply -f deploy/k8s/hpa.yaml
kubectl apply -f deploy/k8s/pdb.yaml
kubectl apply -f deploy/k8s/servicemonitor.yaml  # Prometheus Operator 있을 때만

# 한 번에 전체 적용 (순서 무관하게 처리됨)
kubectl apply -f deploy/k8s/
```

#### 롤아웃 확인

```bash
# 배포 완료 대기
kubectl rollout status deployment/llm-proxy -n llm-proxy

# Pod 상태 확인
kubectl get pods -n llm-proxy -o wide

# 예상 출력
# NAME                        READY   STATUS    RESTARTS   AGE   NODE
# llm-proxy-7d9f8b6c4-xk2pq   1/1     Running   0          30s   node-1
# llm-proxy-7d9f8b6c4-mn3rs   1/1     Running   0          30s   node-2
```

#### 동작 검증

```bash
# 헬스체크 (Pod IP 직접 확인)
kubectl exec -n llm-proxy deploy/llm-proxy -- \
  wget -qO- http://localhost:8080/healthz
# → ok

# 실시간 로그
kubectl logs -n llm-proxy -l app=llm-proxy -f --tail=50

# 메트릭 확인
kubectl port-forward -n llm-proxy svc/llm-proxy 8080:80
curl -s http://localhost:8080/metrics | grep llm_proxy_requests_total
```

#### 이미지 업데이트 (롤링 배포)

```bash
IMAGE=ghcr.io/nhm0819/llm-proxy
TAG=$(git rev-parse --short HEAD)

# Deployment 이미지 교체 → 자동 롤링 업데이트
kubectl set image deployment/llm-proxy \
  llm-proxy=$IMAGE:$TAG \
  -n llm-proxy

# 롤아웃 추적
kubectl rollout status deployment/llm-proxy -n llm-proxy

# 문제 발생 시 직전 버전으로 즉시 롤백
kubectl rollout undo deployment/llm-proxy -n llm-proxy
```

> **Kustomize / Helm 권장**: `kubectl set image`는 간단하지만 GitOps 환경에서는
> `kustomization.yaml`의 `images` 필드나 Helm `values.yaml`의 `image.tag`를
> Git에서 관리하는 방식을 사용하세요.

### 프로덕션 체크리스트

| 항목 | 설명 |
|------|------|
| ✅ Secret 분리 | `DATABASE_URL`, `PROXY_API_KEYS_JSON`, `UPSTREAM_API_KEY`, `AUDIT_HMAC_KEY`를 Secret으로 관리 |
| ✅ PostgreSQL HA | 프로덕션에서는 PG Replication 또는 관리형 DB(RDS, Cloud SQL 등) 사용 |
| ✅ Redis HA | Redis Sentinel 또는 Redis Cluster 사용 (단일 Redis는 SPOF) |
| ✅ 이미지 태그 고정 | `latest` 대신 Git SHA 태그 사용 |
| ✅ Resource 요청/제한 | OOM Kill 방지를 위해 limits 명시 |
| ✅ readinessProbe | 재시작 중 트래픽 차단 |
| ✅ PDB | 노드 점검 중 가용성 보장 |
| ✅ podAntiAffinity | 단일 노드 장애 시 영향 최소화 |
| ✅ TLS | Ingress 레벨에서 TLS 종료 |
| ✅ `proxy-buffering: off` | SSE 스트리밍 응답이 Ingress에서 버퍼링되지 않도록 설정 |
| ✅ `AUDIT_HMAC_KEY` 설정 | 감사 해시 위변조 방지 |
| ✅ Prometheus + Grafana | `llm_proxy_quota_rejected_total` 급증 시 알림 설정 |


---

## 설정

모든 설정은 환경변수로 주입됩니다. 전체 목록은 [ENV.md](./ENV.md)를 참고하세요.

### 최소 필수 설정

```bash
# 업스트림 (단일 백엔드)
UPSTREAM_BASE_URL=https://api.openai.com
UPSTREAM_API_KEY=sk-...

# PostgreSQL (API 키 primary 저장소)
DATABASE_URL=postgres://postgres:postgres@localhost:5432/llm_proxy?sslmode=disable

# Redis (캐시 + 할당량/RPS/감사)
REDIS_ADDR=localhost:6379

# 프록시 인증 키 (빈 값 = 인증 없음, 개발 전용)
PROXY_API_KEYS_JSON='{"sk-proxy-alice":"alice","sk-proxy-bob":"bob"}'
```

### 주요 설정 요약

| 변수 | 기본값 | 설명 |
|------|--------|------|
| `DATABASE_URL` | — | PostgreSQL DSN (`postgres://user:pass@host:5432/db?sslmode=disable`) |
| `TOKEN_DAILY_LIMIT` | `200000` | 사용자별 일일 토큰 한도 |
| `TOKEN_SAFETY_FACTOR` | `1.20` | 토큰 예약 시 안전 계수 (과소 예약 방지) |
| `RATE_LIMIT_RPS` | `5` | 사용자별 초당 요청 수 |
| `PII_BLOCK` | `false` | PII 탐지 시 차단 여부 |
| `QUOTA_TIMEZONE` | `Asia/Seoul` | 일일 할당량 리셋 기준 시간대 |
| `AUDIT_HMAC_KEY` | _(없음)_ | 감사 해시 HMAC 키 (미설정 시 SHA-256) |
| `LOKI_PUSH_URL` | _(없음)_ | Loki 엔드포인트 (미설정 시 비활성) |

---

## API 사용법

프록시는 OpenAI API와 동일한 경로를 지원합니다.

### 지원 엔드포인트

| 경로 | 설명 |
|------|------|
| `POST /v1/chat/completions` | Chat Completions API |
| `POST /v1/completions` | Legacy Completions API |
| `POST /v1/responses` | Responses API |
| `GET  /v1/*` | 기타 GET 요청 (모델 목록 등) — 할당량 적용 안 됨 |
| `GET  /healthz` | 헬스체크 |
| `GET  /metrics` | Prometheus 메트릭 |

### 스트리밍

`"stream": true` 요청을 그대로 지원합니다. SSE 이벤트를 파싱해 usage 정보를 추출하고 할당량을 정산합니다.

```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer sk-proxy-alice" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"hi"}]}'
```

OpenAI의 `stream_options.include_usage` 자동 주입도 지원합니다 (`INJECT_CHAT_STREAM_USAGE=true`).

---

## 모델 라우팅

`ROUTES_JSON`으로 모델명 접두사에 따라 다른 업스트림을 선택합니다. **가장 긴 접두사**가 우선 적용됩니다.

```bash
ROUTES_JSON='[
  {"prefix":"gpt",    "name":"openai",    "base_url":"https://api.openai.com",    "api_key":"sk-..."},
  {"prefix":"claude", "name":"anthropic", "base_url":"https://api.anthropic.com", "api_key":"sk-ant-..."},
  {"prefix":"",       "name":"default",   "base_url":"https://api.openai.com",    "api_key":"sk-..."}
]'
```

| 요청 모델 | 선택 업스트림 |
|-----------|-------------|
| `gpt-4o` | openai |
| `gpt-4-turbo` | openai |
| `claude-3-opus` | anthropic |
| `unknown-model` | default |

빈 접두사(`""`)는 fallback 기본값입니다.

---

## 인증

프록시는 자체 API 키를 발급해 클라이언트를 인증합니다. 업스트림 API 키는 클라이언트에 노출되지 않습니다.

```
클라이언트                프록시                    업스트림
Authorization: Bearer sk-proxy-alice
                    →  검증 후 헤더 삭제
                       Authorization: Bearer sk-openai-real-key  →
```

**키 등록:**

```bash
PROXY_API_KEYS_JSON='{
  "sk-proxy-alice": "alice",
  "sk-proxy-bob":   "bob",
  "sk-proxy-admin": "admin"
}'
```

- `PROXY_API_KEYS_JSON`이 비어있으면 인증을 건너뜁니다 (개발 환경 전용).
- 각 키는 고유한 `userID`에 매핑되며 이 ID가 할당량·RPS 제한·감사 로그에 사용됩니다.

---

## 토큰 할당량

사용자별 일일 토큰 예산을 Redis에서 원자적으로 관리합니다.

### 동작 방식

1. **요청 시 예약**: `promptTokens × SafetyFactor + maxOutputTokens` 만큼 선점
2. **응답 후 정산**: 실제 사용 토큰으로 정산 (차이만큼 반환 또는 추가 차감)
3. **자정 리셋**: Redis 키 TTL이 자정까지의 남은 초로 설정되어 자동 만료

```bash
# 전체 사용자 기본 한도
TOKEN_DAILY_LIMIT=200000

# 사용자별 오버라이드
USER_TOKEN_LIMITS_JSON='{"alice": 1000000, "free-tier-bob": 10000}'

# 할당량 기준 시간대
QUOTA_TIMEZONE=Asia/Seoul
```

할당량 초과 시 `HTTP 429`를 반환하고 남은 용량을 헤더로 전달합니다.

```
HTTP/1.1 429 Too Many Requests
X-Token-Limit: 200000
X-Token-Used: 200000
X-Token-Remaining: 0

{"error": {"type": "rate_limit_error", "message": "token quota exceeded"}}
```

---

## RPS 제한

Redis 고정 윈도우(1초)로 사용자별 초당 요청 수를 제한합니다.

```bash
RATE_LIMIT_RPS=5

# 사용자별 오버라이드
USER_RATE_LIMITS_JSON='{"alice": 50, "free-tier": 1}'
```

초과 시 `HTTP 429`를 반환합니다.

```json
{"error": {"type": "rate_limit_error", "message": "rps rate limit exceeded"}}
```

---

## PII 탐지

요청 본문에서 개인정보를 탐지해 로그에서 마스킹하고, 선택적으로 요청 자체를 차단합니다.

### 내장 패턴

| 패턴 | 설명 |
|------|------|
| `KR_RRN` | 한국 주민등록번호 (체크섬 검증 포함) |
| `EMAIL` | 이메일 주소 |
| `KR_PHONE` | 한국 휴대폰 번호 (01x 형식) |

```bash
# 탐지만 (기본): 로그에 마스킹하여 기록, 요청은 통과
PII_BLOCK=false

# 차단: PII 탐지 시 HTTP 400 반환
PII_BLOCK=true
```

탐지된 PII는 로그에서 `[REDACTED:EMAIL]` 형태로 치환됩니다. 원본 텍스트는 해시(HMAC)로만 기록됩니다.

---

## 감사 로그

모든 요청·응답의 콘텐츠 해시를 Redis Stream에 기록합니다. 원본 텍스트를 저장하지 않고도 사후 검증이 가능합니다.

```bash
AUDIT_STREAM_KEY=audit:llm-proxy
AUDIT_RECORD_TTL_SECONDS=604800   # 7일
AUDIT_STORE_REQUEST_RECORD=true   # 요청별 Redis Hash 저장
AUDIT_HMAC_KEY=your-secret-key    # 미설정 시 SHA-256, 설정 시 HMAC-SHA256
```

### Redis Stream 레코드 구조

```
XRANGE audit:llm-proxy - +

1) ts            "2024-03-15T09:00:00.000Z"
2) request_id    "3f2a1b..."
3) user_id       "alice"
4) kind          "chat"
5) model         "gpt-4o"
6) upstream      "openai"
7) status        "200"
8) request_hash  "sha256:abc..."
9) response_hash "sha256:def..."
10) total_tokens "312"
11) pii_found    "false"
```

감사 레코드 조회:

```bash
# 최근 100건
redis-cli XREVRANGE audit:llm-proxy + - COUNT 100

# 특정 요청 상세
redis-cli HGETALL audit:req:<request-id>
```

---

## 관찰 가능성

### Prometheus 메트릭 (`/metrics`)

| 메트릭 | 타입 | 레이블 |
|--------|------|--------|
| `llm_proxy_requests_total` | Counter | `status`, `kind`, `upstream`, `user_id` |
| `llm_proxy_request_duration_seconds` | Histogram | `kind`, `upstream` |
| `llm_proxy_tokens_used_total` | Counter | `user_id`, `kind`, `model` |
| `llm_proxy_quota_rejected_total` | Counter | `user_id` |
| `llm_proxy_rps_rejected_total` | Counter | `user_id` |
| `llm_proxy_pii_detected_total` | Counter | `kind`, `pii_type`, `direction` |
| `llm_proxy_circuit_state` | Gauge | `upstream` (0=closed, 1=open, 2=half-open) |

### Loki 구조화 로그

```bash
LOKI_PUSH_URL=http://loki:3100/loki/api/v1/push
LOKI_LABELS=app=llm-proxy,env=production
```

각 요청이 JSON 한 줄로 Loki에 전송됩니다.

```json
{
  "time": "2024-03-15T09:00:00.000Z",
  "request_id": "3f2a1b...",
  "user_id": "alice",
  "kind": "chat",
  "model": "gpt-4o",
  "upstream": "openai",
  "status": 200,
  "duration_ms": 1234,
  "pii_found": false,
  "prompt_tokens_est": 25,
  "actual_total_tokens": 312,
  "token_limit": 200000,
  "token_used": 312,
  "token_remaining": 199688
}
```

`docker compose up` 시 Grafana(`http://localhost:3000`)에서 Prometheus와 Loki 데이터소스가 자동으로 등록되어 별도 설정 없이 바로 조회할 수 있습니다. Grafana Alloy가 `proxy:8080/metrics`를 수집해 Prometheus로 remote_write하며, Docker 컨테이너 로그는 Loki로 전달됩니다.

---

## 회로 차단기

업스트림 장애 시 자동으로 요청을 차단해 시스템을 보호합니다.

```
정상 (Closed)
    │ 연속 5회 실패
    ▼
차단 (Open) ──30초 경과──▶ 탐색 (Half-Open)
    ▲                            │ 성공
    │ 실패                       ▼
    └──────────────────── 정상 (Closed)
```

`http.RoundTripper`로 구현되어 `HTTPClient.Transport`에 자동 적용됩니다. 업스트림별로 독립적인 회로를 유지합니다.

회로 상태는 Prometheus `llm_proxy_circuit_state` 게이지로 확인할 수 있습니다.

---

## 개발 & 테스트

로컬에서 코드를 수정하고 테스트한 뒤 Docker 이미지로 빌드해 Kubernetes에 배포하는 흐름으로 사용합니다.

### 사전 요구사항

- Go 1.25+
- PostgreSQL 15+
- Redis 7+
- Docker (apikey 통합 테스트에 필요; 없으면 해당 테스트 자동 skip)

### 로컬 실행

```bash
# 의존성 설치
go mod download

# PostgreSQL 실행 (Docker)
docker run -d -p 5432:5432 \
  -e POSTGRES_PASSWORD=postgres \
  -e POSTGRES_DB=llm_proxy \
  postgres:17-alpine

# Redis 실행 (Docker)
docker run -d -p 6379:6379 redis:7-alpine

# 프록시 실행 (인증 없음, dev mode)
DATABASE_URL=postgres://postgres:postgres@localhost:5432/llm_proxy?sslmode=disable \
UPSTREAM_API_KEY=sk-... \
UPSTREAM_BASE_URL=https://api.openai.com \
go run ./cmd/proxy
```

### 테스트

```bash
# 전체 테스트 (apikey 패키지는 Docker 필요 — 없으면 자동 skip)
go test ./...

# Docker 없는 환경: 통합 테스트 제외
go test -short ./...

# 레이스 컨디션 검사
go test -race ./...

# 커버리지 리포트
go test -coverprofile=coverage.out ./...
go tool cover -html=coverage.out
```

### 테스트 커버리지 범위

현재 커버리지: **73.4%** (이전 54.5%에서 +18.9pp 향상)

| 패키지 | 주요 테스트 |
|--------|------------|
| `httputil` | CopyHeaders, RespondErr, NewRequestID (7개) |
| `config` | Load() 기본값, 환경변수 오버라이드, ROUTES_JSON 파싱 (7개) |
| `pii` | 이메일·전화·RRN 체크섬, 빈 입력, 플레이스홀더 형식 |
| `router` | 최장 접두사 우선, 빈 모델 fallback |
| `tokencount` | Count 함수, 캐시 히트, fallback (6개) |
| `quota` | Lua 스크립트 원자성, DayKey 형식, TTL, 음수 clamp, concurrent Reserve (4개 추가) |
| `ratelimit` | RPS 제한, 사용자 독립성, TTL 만료 후 리셋, limit=1 엣지 케이스 (2개 추가) |
| `audit` | Stream 기록, HMAC vs SHA-256, TTL 검증 |
| `loki` | Push no-op, httptest mock, 라벨 머지, 에러 처리 (5개) |
| `metrics` | custom registry로 메트릭 등록/기록 검증 (7개) |
| `docs` | spec/UI 엔드포인트, Cache-Control 헤더 (3개) |
| `breaker` | Closed→Open→HalfOpen 전이, per-host 독립 circuit, probe fail reopens, State String (5개 추가) |
| `middleware` | RequestID, Recover, MaxBody, Chain, AccessLog, 키 검증, userID 주입 (9개) |
| `apikey` | testcontainers-go (PostgreSQL 컨테이너) + miniredis; Docker 없으면 자동 skip |
| `apikey` (handler) | Admin REST API CRUD + 인증 + disabled + method not allowed (12개, Docker skip) |
| `proxy/classify` | 경로 분류, 메시지 텍스트 추출, 토큰 파라미터 파싱 |
| `proxy/stream` | SSE 이벤트 파싱, 엑서프트 트런케이션 |
| `proxy` (통합) | E2E: 성공·429·PII 차단·스트리밍·동시성 20개 고루틴 |

### 빌드

```bash
# 일반 빌드
go build -o llm-proxy ./cmd/proxy

# 최소 크기 (distroless 배포용)
CGO_ENABLED=0 GOOS=linux go build \
  -trimpath -ldflags="-s -w" \
  -o llm-proxy ./cmd/proxy

# Docker 이미지
docker build -t llm-proxy:latest .
```

---

---

---

## 패키지 구조

```
llm-proxy/
├── cmd/
│   └── proxy/
│       └── main.go                  진입점 · DI 조립
├── internal/
│   ├── apikey/
│   │   ├── store.go                 PG primary + Redis cache 기반 API 키 CRUD + Resolve
│   │   ├── handler.go               Admin HTTP 핸들러 (/admin/keys)
│   │   ├── migrate.go               go:embed + RunMigrations
│   │   ├── store_test.go            통합 테스트 (testcontainers-go + miniredis)
│   │   └── migrations/
│   │       └── 001_create_api_keys.sql
│   ├── config/
│   │   ├── config.go                환경변수 로딩
│   │   └── validate.go              시작 시 검증
│   ├── router/
│   │   └── router.go                모델명 → 업스트림 라우팅
│   ├── pii/
│   │   └── pii.go                   PII 탐지 & 마스킹
│   ├── tokencount/
│   │   └── tokencount.go            tiktoken 토큰 카운터
│   ├── quota/
│   │   └── quota.go                 Redis 일일 할당량
│   ├── ratelimit/
│   │   └── ratelimit.go             Redis RPS 제한
│   ├── audit/
│   │   └── audit.go                 Redis Stream 감사 + HashBuilder
│   ├── loki/
│   │   └── loki.go                  Loki 로그 전송
│   ├── breaker/
│   │   └── breaker.go               회로 차단기
│   ├── metrics/
│   │   └── metrics.go               Prometheus 메트릭
│   ├── middleware/
│   │   ├── middleware.go            Recover · RequestID · AccessLog · MaxBody
│   │   └── auth.go                  API 키 인증
│   └── proxy/
│       ├── proxy.go                 핵심 ServeHTTP
│       ├── classifier.go            경로·필드 분류·추출 헬퍼
│       └── streamer.go              SSE 파싱 + StreamEventParser
└── pkg/
    └── httputil/
        └── httputil.go              공유 HTTP 유틸리티
```

---

## 라이선스

MIT
