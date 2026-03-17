# 브랜치 전략 (GitLab Flow)

## 개요

`dev → main` 단방향 흐름을 기반으로, **태그**로 배포 환경을 구분합니다.
GitLab CI/CD 파이프라인은 태그 패턴과 브랜치를 조합해 자동으로 적절한 환경에 배포합니다.

```
feature/* ─┐
hotfix/*  ─┤──→ dev ──(PR)──→ main
           │         ↑tag rc       ↑tag release
           │       dev 배포      prod 배포
```

---

## 브랜치 구성

| 브랜치 | 목적 | 보호 여부 |
|--------|------|-----------|
| `main` | 프로덕션 기준. 항상 배포 가능한 상태 유지 | Protected (force push 금지, MR 필수) |
| `dev` | 통합 개발. 피처 병합 후 RC 태그로 스테이징 배포 | Protected (MR 필수) |
| `feature/*` | 기능 단위 작업 브랜치. `dev`에서 분기, `dev`로 MR | 자유 |
| `hotfix/*` | 프로덕션 긴급 수정. `main`에서 분기, `main` + `dev` 양쪽으로 MR | 자유 |

---

## 태그 전략

### RC 태그 → Dev 환경 배포

```
형식: v{MAJOR}.{MINOR}.{PATCH}-rc{N}
예시: v0.1.0-rc1  v0.1.2-rc3  v1.0.0-rc1
```

- `dev` 브랜치에서만 생성
- 통합 테스트, QA 검증 목적
- 동일 버전에서 여러 RC 반복 가능 (`rc1 → rc2 → ...`)

### Release 태그 → Production 환경 배포

```
형식: v{MAJOR}.{MINOR}.{PATCH}
예시: v0.1.0  v0.1.2  v1.0.0
```

- `main` 브랜치에서만 생성
- RC 검증이 완료된 커밋에만 태깅
- Semantic Versioning 준수

---

## 작업 흐름

### 1. 일반 기능 개발

```bash
# 1. dev 기반 피처 브랜치 생성
git checkout dev
git pull
git checkout -b feature/add-retry-logic

# 2. 작업 후 dev로 MR
git push origin feature/add-retry-logic
# GitLab: feature/add-retry-logic → dev 로 MR 생성

# 3. MR 승인 & 병합 후 RC 태그
git checkout dev && git pull
git tag v0.1.1-rc1
git push origin v0.1.1-rc1
# → CI: dev 환경 자동 배포
```

### 2. QA 통과 → 프로덕션 배포

```bash
# 1. dev → main MR 생성 & 승인 & 병합
# GitLab: dev → main 로 MR 생성

# 2. main 최신화 후 release 태그
git checkout main && git pull
git tag v0.1.1
git push origin v0.1.1
# → CI: production 환경 자동 배포
```

### 3. 핫픽스

```bash
# 1. main 기반 hotfix 브랜치
git checkout main && git pull
git checkout -b hotfix/fix-auth-crash

# 2. main으로 MR → 병합 후 즉시 release 태그
git checkout main && git pull
git tag v0.1.2
git push origin v0.1.2
# → CI: production 배포

# 3. 같은 픽스를 dev에도 MR (충돌 없으면 cherry-pick 활용)
git checkout dev && git pull
git checkout -b hotfix/fix-auth-crash-dev
git cherry-pick <commit-sha>
git push origin hotfix/fix-auth-crash-dev
# GitLab: hotfix/fix-auth-crash-dev → dev 로 MR 생성
```

---

## GitLab CI/CD 파이프라인 예시

```yaml
# .gitlab-ci.yml

stages:
  - test
  - build
  - deploy

variables:
  IMAGE_REGISTRY: registry.gitlab.com/$CI_PROJECT_PATH
  IMAGE_TAG: $CI_COMMIT_TAG   # 태그가 없으면 빈 문자열

# ── 테스트: 모든 브랜치 & 태그 ───────────────────────────────
test:
  stage: test
  image: golang:1.25
  script:
    - go test -race ./...
  rules:
    - if: $CI_COMMIT_BRANCH
    - if: $CI_COMMIT_TAG

# ── 이미지 빌드: RC 또는 Release 태그에서만 ──────────────────
build:
  stage: build
  image: docker:latest
  services:
    - docker:dind
  script:
    - docker build -t $IMAGE_REGISTRY:$CI_COMMIT_TAG .
    - docker push $IMAGE_REGISTRY:$CI_COMMIT_TAG
  rules:
    # v0.1.0-rc1 형식 (dev 브랜치에서 생성)
    - if: '$CI_COMMIT_TAG =~ /^v\d+\.\d+\.\d+-rc\d+$/'
    # v0.1.0 형식 (main 브랜치에서 생성)
    - if: '$CI_COMMIT_TAG =~ /^v\d+\.\d+\.\d+$/'

# ── Dev 배포: RC 태그 ─────────────────────────────────────────
deploy:dev:
  stage: deploy
  environment:
    name: dev
    url: https://dev.llm-proxy.internal
  script:
    - echo "Deploying $CI_COMMIT_TAG to dev..."
    # ex) kubectl set image deployment/llm-proxy llm-proxy=$IMAGE_REGISTRY:$CI_COMMIT_TAG -n dev
  rules:
    - if: '$CI_COMMIT_TAG =~ /^v\d+\.\d+\.\d+-rc\d+$/'

# ── Production 배포: Release 태그 + main 브랜치 ───────────────
deploy:prod:
  stage: deploy
  environment:
    name: production
    url: https://llm-proxy.internal
  script:
    - echo "Deploying $CI_COMMIT_TAG to production..."
    # ex) kubectl set image deployment/llm-proxy llm-proxy=$IMAGE_REGISTRY:$CI_COMMIT_TAG -n prod
  rules:
    - if: '$CI_COMMIT_TAG =~ /^v\d+\.\d+\.\d+$/ && $CI_COMMIT_BRANCH == "main"'
```

> **참고**: `$CI_COMMIT_BRANCH`는 태그 파이프라인에서 `null`이 될 수 있으므로, Release 태그는 **반드시 main 브랜치에서** 생성해야 의도한 조건이 동작합니다.

---

## 버전 규칙 요약

| 변경 유형 | 예시 | 버전 올림 |
|-----------|------|-----------|
| 호환성 깨지는 변경 | 인증 방식 교체 | MAJOR (`v1.0.0`) |
| 하위 호환 기능 추가 | 새 어드민 API | MINOR (`v0.2.0`) |
| 버그 수정 / 마이너 개선 | 핫픽스, 설정 변경 | PATCH (`v0.1.1`) |

---

## GitLab 설정 체크리스트

- [ ] `main` 브랜치 보호: MR 필수, force push 금지
- [ ] `dev` 브랜치 보호: MR 필수
- [ ] RC 태그(`v*-rc*`)는 Developer 이상만 생성 가능
- [ ] Release 태그(`v[0-9]*`)는 Maintainer 이상만 생성 가능
- [ ] MR 병합 시 소스 브랜치 자동 삭제 활성화
- [ ] `main` MR은 최소 1명 승인 필수
