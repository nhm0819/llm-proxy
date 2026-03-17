#!/usr/bin/env python3
"""
LLM Code Review — GitLab CI 전용 스크립트
모델: Qwen3.5-122B-A10B (262,144 token native context, thinking mode 지원)

동작 방식:
  1. git diff로 변경 코드 추출 (*.go 전체, 테스트 포함)
  2. vLLM (OpenAI-compatible) API에 코드 리뷰 요청
     - thinking mode 활성화 시 reasoning_content도 수집
  3. GitLab MR에 코멘트로 결과 등록
     - thinking 내용은 <details> 접기 블록으로 표시

컨텍스트 예산 (native 262,144 tokens 기준):
  시스템 프롬프트    :   ~700 tokens
  응답 (max_tokens) :  8,192 tokens
  thinking budget   :  8,192 tokens (선택)
  안전 버퍼         :  5,000 tokens
  diff 가용          : ~240,000 tokens ≈ 15,000줄 (평균 16 tokens/줄 기준)

필요 환경변수:
  필수:
    LLM_API_BASE_URL     vLLM 엔드포인트 (예: http://vllm.internal:8000)
    LLM_API_KEY          LLM API 키 (또는 프록시 키)
    LLM_MODEL            모델명 (예: Qwen3.5-122B-A10B)
    GITLAB_TOKEN         GitLab API 토큰 (MR 코멘트 권한 필요)
    CI_PROJECT_ID        GitLab 프로젝트 ID (CI 자동 제공)
    CI_MERGE_REQUEST_IID MR 번호 (CI 자동 제공)

  선택:
    LLM_MAX_DIFF_LINES   diff 최대 라인 수 (기본: 15000)
    LLM_REVIEW_LANG      리뷰 언어 ko|en (기본: ko)
    LLM_THINKING         thinking mode 활성화 true|false (기본: true)
    LLM_THINKING_BUDGET  thinking 최대 토큰 (기본: 8192, -1=무제한)
    CI_SERVER_URL        GitLab 서버 URL (기본: https://gitlab.com)
    GITLAB_API_V4_URL    GitLab API URL (CI 자동 제공)
"""

import json
import os
import subprocess
import sys
import textwrap
import urllib.error
import urllib.request


# ── 설정 ──────────────────────────────────────────────────────

API_BASE         = os.environ.get("LLM_API_BASE_URL", "").rstrip("/")
API_KEY          = os.environ.get("LLM_API_KEY", "")
MODEL            = os.environ.get("LLM_MODEL", "")
GL_TOKEN         = os.environ.get("GITLAB_TOKEN", "")
PROJECT_ID       = os.environ.get("CI_PROJECT_ID", "")
MR_IID           = os.environ.get("CI_MERGE_REQUEST_IID", "")
GL_API_URL       = os.environ.get(
    "GITLAB_API_V4_URL",
    os.environ.get("CI_SERVER_URL", "https://gitlab.com").rstrip("/") + "/api/v4",
)
MAX_LINES        = int(os.environ.get("LLM_MAX_DIFF_LINES", "15000"))
LANG             = os.environ.get("LLM_REVIEW_LANG", "ko")
THINKING_ENABLED = os.environ.get("LLM_THINKING", "true").lower() == "true"
THINKING_BUDGET  = int(os.environ.get("LLM_THINKING_BUDGET", "8192"))  # -1 = unlimited


# ── 시스템 프롬프트 ────────────────────────────────────────────

SYSTEM_PROMPT_KO = textwrap.dedent("""\
    당신은 Go 언어 전문 시니어 소프트웨어 엔지니어입니다.
    아래 git diff를 빠짐없이 분석해서 코드 리뷰를 작성하세요.
    변경된 모든 파일(프로덕션 코드 + 테스트)을 함께 검토합니다.

    리뷰 항목 (발견된 항목만 포함):
    - 🐛 **버그 / 로직 오류**: 잠재적 패닉, nil 역참조, 경쟁 조건, 엣지 케이스 미처리
    - 🔒 **보안**: SQL 인젝션, SSRF, 민감 정보 노출, 인증·권한 우회, HMAC/암호화 오용
    - ⚡ **성능**: 불필요한 메모리 할당, 루프 내 I/O·락, 비효율적 자료구조
    - 🧹 **코드 품질**: 네이밍, 복잡도, Go 관용구 위반, 에러 처리 누락, 데드코드
    - ✅ **테스트**: 커버되지 않은 케이스, 테스트 가능성 저하, 잘못된 assertion

    출력 형식:
    - 항목별 마크다운 헤더 사용
    - 각 이슈마다 `파일명:라인번호` 명시
    - 구체적인 개선 코드 스니펫 제안 (있을 경우)
    - 심각도: 🔴 Critical / 🟡 Warning / 🔵 Info
    - 마지막에 **한 줄 요약** (전체 이슈 수, 최고 심각도 포함)

    이슈가 없으면 "✅ 특이사항 없음" 만 출력하세요.
""")

SYSTEM_PROMPT_EN = textwrap.dedent("""\
    You are a senior Go software engineer performing a thorough code review.
    Analyze the complete git diff below, covering both production code and tests.

    Review categories (include only what you find):
    - 🐛 **Bugs / Logic errors**: Potential panics, nil dereference, race conditions, unhandled edge cases
    - 🔒 **Security**: Injection, SSRF, sensitive data exposure, auth bypass, HMAC/crypto misuse
    - ⚡ **Performance**: Unnecessary allocations, I/O in loops, lock contention, inefficient data structures
    - 🧹 **Code quality**: Naming, complexity, Go idiom violations, missing error handling, dead code
    - ✅ **Tests**: Uncovered paths, poor testability, incorrect assertions

    Format:
    - Markdown headers per category
    - `file:line` reference for each issue
    - Concrete code suggestion when applicable
    - Severity: 🔴 Critical / 🟡 Warning / 🔵 Info
    - **One-line summary** at the end (total issues, highest severity)

    If nothing to report, output only: "✅ No issues found"
""")

SYSTEM_PROMPT = SYSTEM_PROMPT_KO if LANG == "ko" else SYSTEM_PROMPT_EN


# ── 유틸 ──────────────────────────────────────────────────────

def check_env() -> None:
    missing = [v for v in ("LLM_API_BASE_URL", "LLM_API_KEY", "LLM_MODEL",
                            "GITLAB_TOKEN", "CI_PROJECT_ID", "CI_MERGE_REQUEST_IID")
               if not os.environ.get(v)]
    if missing:
        print(f"[llm-review] 필수 환경변수 누락: {', '.join(missing)}", file=sys.stderr)
        sys.exit(1)


def get_diff() -> str:
    """MR base → HEAD diff를 추출합니다. Go 파일 전체 포함 (테스트 파일 포함)."""
    base_sha = os.environ.get("CI_MERGE_REQUEST_DIFF_BASE_SHA", "")
    head_sha = os.environ.get("CI_COMMIT_SHA", "HEAD")

    if base_sha:
        cmd = ["git", "diff", base_sha, head_sha, "--", "*.go"]
    else:
        cmd = ["git", "diff", "HEAD~1", "HEAD", "--", "*.go"]

    result = subprocess.run(cmd, capture_output=True, text=True)
    if result.returncode != 0:
        print(f"[llm-review] git diff 실패: {result.stderr}", file=sys.stderr)
        sys.exit(1)

    return result.stdout


def truncate_diff(diff: str, max_lines: int) -> tuple[str, bool]:
    lines = diff.splitlines()
    if len(lines) <= max_lines:
        return diff, False
    truncated = "\n".join(lines[:max_lines])
    return truncated, True


def call_llm(diff: str) -> tuple[str, str | None]:
    """
    OpenAI-compatible chat completions API 호출.
    반환: (content, reasoning_content | None)
    """
    payload: dict = {
        "model": MODEL,
        "messages": [
            {"role": "system", "content": SYSTEM_PROMPT},
            {"role": "user",   "content": f"```diff\n{diff}\n```"},
        ],
        "temperature": 0.6,
        "max_tokens": 8192,
    }

    # Qwen3 thinking mode — vLLM은 chat_template_kwargs로 활성화
    if THINKING_ENABLED:
        payload["chat_template_kwargs"] = {"enable_thinking": True}
        if THINKING_BUDGET != -1:
            # vLLM >= 0.8: thinking_budget 지원
            payload["thinking"] = {"type": "enabled", "budget_tokens": THINKING_BUDGET}

    data = json.dumps(payload).encode()
    req = urllib.request.Request(
        url=f"{API_BASE}/v1/chat/completions",
        data=data,
        headers={
            "Content-Type": "application/json",
            "Authorization": f"Bearer {API_KEY}",
        },
        method="POST",
    )
    try:
        with urllib.request.urlopen(req, timeout=300) as resp:
            body = json.loads(resp.read())
            msg = body["choices"][0]["message"]
            content = msg.get("content", "").strip()
            reasoning = msg.get("reasoning_content", None)
            if reasoning:
                reasoning = reasoning.strip()
            return content, reasoning
    except urllib.error.HTTPError as e:
        err_body = e.read().decode(errors="replace")
        print(f"[llm-review] LLM API 오류 {e.code}: {err_body}", file=sys.stderr)
        sys.exit(1)
    except Exception as e:
        print(f"[llm-review] LLM 호출 실패: {e}", file=sys.stderr)
        sys.exit(1)


def post_mr_comment(body: str) -> None:
    """GitLab MR에 코멘트를 등록합니다."""
    url = f"{GL_API_URL}/projects/{PROJECT_ID}/merge_requests/{MR_IID}/notes"
    payload = json.dumps({"body": body}).encode()
    req = urllib.request.Request(
        url=url,
        data=payload,
        headers={
            "Content-Type": "application/json",
            "PRIVATE-TOKEN": GL_TOKEN,
        },
        method="POST",
    )
    try:
        with urllib.request.urlopen(req, timeout=30) as resp:
            if resp.status == 201:
                print("[llm-review] MR 코멘트 등록 완료")
            else:
                print(f"[llm-review] 코멘트 등록 상태: {resp.status}")
    except urllib.error.HTTPError as e:
        err_body = e.read().decode(errors="replace")
        print(f"[llm-review] GitLab API 오류 {e.code}: {err_body}", file=sys.stderr)
        sys.exit(1)


def build_comment(
    review: str,
    reasoning: str | None,
    truncated: bool,
    diff_lines: int,
) -> str:
    thinking_info = " · thinking ✅" if THINKING_ENABLED else ""
    meta = (
        f"> **모델**: `{MODEL}`{thinking_info} | "
        f"**분석 라인**: {diff_lines:,}"
    )
    if truncated:
        meta += f" (상위 {MAX_LINES:,}줄까지 분석)"
    meta += "\n\n---\n\n"

    body = "## 🤖 LLM 코드 리뷰\n\n" + meta + review

    # thinking 내용은 접기 블록으로 추가 (너무 길어서 기본 접힘)
    if reasoning:
        body += (
            "\n\n<details>\n<summary>🧠 Thinking (추론 과정 펼치기)</summary>\n\n"
            f"```\n{reasoning}\n```\n\n</details>"
        )

    return body


# ── 메인 ──────────────────────────────────────────────────────

def main() -> None:
    check_env()

    print("[llm-review] diff 추출 중...")
    raw_diff = get_diff()
    if not raw_diff.strip():
        print("[llm-review] Go 파일 변경 없음, 스킵")
        return

    diff, truncated = truncate_diff(raw_diff, MAX_LINES)
    diff_lines = len(raw_diff.splitlines())
    print(
        f"[llm-review] diff {diff_lines:,}줄 | "
        f"truncated={truncated} | thinking={THINKING_ENABLED}"
    )

    print(f"[llm-review] {MODEL} 에 리뷰 요청 중...")
    review, reasoning = call_llm(diff)

    comment = build_comment(review, reasoning, truncated, diff_lines)
    print("\n" + "─" * 60)
    print(comment[:2000], "..." if len(comment) > 2000 else "")
    print("─" * 60 + "\n")

    print("[llm-review] GitLab MR 코멘트 등록 중...")
    post_mr_comment(comment)


if __name__ == "__main__":
    main()
