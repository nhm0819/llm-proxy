#!/usr/bin/env python3
"""
LLM Code Review — GitLab CI 전용 스크립트
모델: Qwen3.5-122B-A10B (262,144 token native context, thinking mode 지원)

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
    LLM_CONTEXT_LINES    git diff -U 값 (기본: 15)
    LLM_REVIEW_LANG      리뷰 언어 ko|en (기본: ko)
    LLM_THINKING         thinking mode true|false (기본: true)
    LLM_THINKING_BUDGET  thinking 최대 토큰 (기본: 8192, -1=무제한)
    CI_SERVER_URL        GitLab 서버 URL (기본: https://gitlab.com)
    GITLAB_API_V4_URL    GitLab API URL (CI 자동 제공)
"""

from __future__ import annotations

import json
import os
import re
import subprocess
import sys
import textwrap
import urllib.error
import urllib.request
from dataclasses import dataclass, field


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
CONTEXT_LINES    = int(os.environ.get("LLM_CONTEXT_LINES", "15"))
LANG             = os.environ.get("LLM_REVIEW_LANG", "ko")
THINKING_ENABLED = os.environ.get("LLM_THINKING", "true").lower() == "true"
THINKING_BUDGET  = int(os.environ.get("LLM_THINKING_BUDGET", "8192"))

# ── diff 필터링 설정 ───────────────────────────────────────────

# git pathspec — 리뷰 대상에서 제외할 파일
EXCLUDE_PATHSPECS = [
    ":!vendor/**",           # Go vendor 디렉터리
    ":!**/*.pb.go",          # protobuf 생성 코드
    ":!**/*_grpc.pb.go",     # gRPC 생성 코드
    ":!**/mock_*.go",        # mockery / gomock 생성 모의 객체
    ":!**/*_mock.go",
    ":!**/*_gen.go",         # go generate 생성 코드
    ":!**/zz_generated*.go", # controller-gen 생성 코드
    ":!**/*.generated.go",
]

# 파일이 trivial(주석·공백·import만 변경)로 간주되는 최소 실질 추가 라인 수
TRIVIAL_THRESHOLD = 3


# ── 시스템 프롬프트 ────────────────────────────────────────────

SYSTEM_PROMPT_KO = textwrap.dedent("""\
    당신은 Go 언어 전문 시니어 소프트웨어 엔지니어입니다.
    아래 git diff를 빠짐없이 분석해서 코드 리뷰를 작성하세요.
    각 파일 diff에는 변경 전후 15줄의 컨텍스트가 포함되어 있습니다.

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
    - 마지막에 **한 줄 요약** (전체 이슈 수, 최고 심각도)

    이슈가 없으면 "✅ 특이사항 없음" 만 출력하세요.
""")

SYSTEM_PROMPT_EN = textwrap.dedent("""\
    You are a senior Go software engineer performing a thorough code review.
    The diff includes 15 lines of context around each change for better comprehension.

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


# ── diff 파싱 & 스마트 필터링 ─────────────────────────────────

@dataclass
class FileDiff:
    path: str
    header: str           # diff --git ... 헤더
    content: str          # 실제 diff 내용
    added: int = 0        # '+' 로 시작하는 실질 추가 라인 수
    removed: int = 0      # '-' 로 시작하는 실질 삭제 라인 수
    trivial: bool = False # 주석·공백·import만 변경된 경우
    lines: int = field(init=False)

    def __post_init__(self) -> None:
        self.lines = len((self.header + self.content).splitlines())


_DIFF_FILE_RE = re.compile(r"^diff --git a/(.+?) b/(.+?)$", re.MULTILINE)
_TRIVIAL_LINE_RE = re.compile(r"^[+-]\s*(//|import\b|\s*$)")


def parse_file_diffs(raw: str) -> list[FileDiff]:
    """unified diff를 파일별 FileDiff 리스트로 파싱합니다."""
    segments: list[tuple[int, int]] = []
    for m in _DIFF_FILE_RE.finditer(raw):
        segments.append((m.start(), m.end()))

    file_diffs: list[FileDiff] = []
    for i, (start, _) in enumerate(segments):
        end = segments[i + 1][0] if i + 1 < len(segments) else len(raw)
        block = raw[start:end]

        # 헤더(diff/index/---/+++) 와 본문 분리
        lines = block.splitlines(keepends=True)
        header_lines, body_lines = [], []
        in_header = True
        for line in lines:
            if in_header and (line.startswith("@@") or (not line.startswith(("diff ", "index ", "--- ", "+++ ", "new ", "deleted ", "old ")))):
                in_header = False
            (header_lines if in_header else body_lines).append(line)

        header = "".join(header_lines)
        content = "".join(body_lines)
        body_text = content

        # 추가/삭제 라인 계산 (파일 메타 헤더 제외)
        added = sum(
            1 for l in body_text.splitlines()
            if l.startswith("+") and not l.startswith("+++")
        )
        removed = sum(
            1 for l in body_text.splitlines()
            if l.startswith("-") and not l.startswith("---")
        )

        # trivial 판정: 실질 추가 라인이 모두 주석/공백/import이면 trivial
        substantive_adds = [
            l for l in body_text.splitlines()
            if l.startswith("+") and not l.startswith("+++")
            and not _TRIVIAL_LINE_RE.match(l)
        ]
        trivial = len(substantive_adds) < TRIVIAL_THRESHOLD

        # path 추출
        m = _DIFF_FILE_RE.match(block)
        path = m.group(2) if m else "unknown"

        file_diffs.append(FileDiff(
            path=path,
            header=header,
            content=content,
            added=added,
            removed=removed,
            trivial=trivial,
        ))

    return file_diffs


def prioritize(file_diffs: list[FileDiff]) -> list[FileDiff]:
    """
    우선순위 정렬:
      1. non-trivial 파일 → 추가 라인 수 내림차순
      2. trivial 파일 → 추가 라인 수 내림차순
    """
    non_trivial = sorted(
        [f for f in file_diffs if not f.trivial],
        key=lambda f: f.added, reverse=True,
    )
    trivial = sorted(
        [f for f in file_diffs if f.trivial],
        key=lambda f: f.added, reverse=True,
    )
    return non_trivial + trivial


def budget_fill(
    ordered: list[FileDiff],
    max_lines: int,
) -> tuple[list[FileDiff], list[FileDiff]]:
    """
    파일 단위로 예산을 채웁니다.
    파일 중간에서 잘리지 않도록 파일 전체가 들어가는 경우만 포함.
    예산이 남았지만 남은 파일이 너무 크면 건너뜁니다.
    """
    included: list[FileDiff] = []
    skipped: list[FileDiff] = []
    remaining = max_lines

    for fd in ordered:
        if fd.lines <= remaining:
            included.append(fd)
            remaining -= fd.lines
        else:
            skipped.append(fd)

    return included, skipped


def build_diff_summary(
    all_files: list[FileDiff],
    skipped: list[FileDiff],
) -> str:
    """모든 변경 파일의 요약 테이블을 생성합니다."""
    lines = ["### 변경 파일 요약\n",
             "| 파일 | +추가 | -삭제 | 분류 | 분석 |",
             "|------|------:|------:|------|------|"]
    skipped_paths = {f.path for f in skipped}
    for fd in all_files:
        kind = "💬 trivial" if fd.trivial else "📝 코드"
        analyzed = "⏭ 건너뜀 (예산 초과)" if fd.path in skipped_paths else "✅"
        lines.append(f"| `{fd.path}` | +{fd.added} | -{fd.removed} | {kind} | {analyzed} |")
    return "\n".join(lines)


# ── 유틸 ──────────────────────────────────────────────────────

def check_env() -> None:
    missing = [v for v in ("LLM_API_BASE_URL", "LLM_API_KEY", "LLM_MODEL",
                            "GITLAB_TOKEN", "CI_PROJECT_ID", "CI_MERGE_REQUEST_IID")
               if not os.environ.get(v)]
    if missing:
        print(f"[llm-review] 필수 환경변수 누락: {', '.join(missing)}", file=sys.stderr)
        sys.exit(1)


def get_diff() -> str:
    """
    MR base → HEAD diff를 추출합니다.

    필터링 전략:
    - Go 파일만 포함 (*.go)
    - 생성 코드·vendor 제외 (EXCLUDE_PATHSPECS)
    - -U{CONTEXT_LINES}: 변경 전후 충분한 컨텍스트 포함
    """
    base_sha = os.environ.get("CI_MERGE_REQUEST_DIFF_BASE_SHA", "")
    head_sha = os.environ.get("CI_COMMIT_SHA", "HEAD")

    base = base_sha if base_sha else "HEAD~1"
    cmd = [
        "git", "diff",
        f"-U{CONTEXT_LINES}",   # 변경 전후 N줄 컨텍스트
        base, head_sha,
        "--",
        "*.go",
        *EXCLUDE_PATHSPECS,
    ]

    result = subprocess.run(cmd, capture_output=True, text=True)
    if result.returncode != 0:
        print(f"[llm-review] git diff 실패: {result.stderr}", file=sys.stderr)
        sys.exit(1)

    return result.stdout


# ── LLM 호출 ──────────────────────────────────────────────────

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

    if THINKING_ENABLED:
        payload["chat_template_kwargs"] = {"enable_thinking": True}
        if THINKING_BUDGET != -1:
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
            reasoning = (msg.get("reasoning_content") or "").strip() or None
            return content, reasoning
    except urllib.error.HTTPError as e:
        err_body = e.read().decode(errors="replace")
        print(f"[llm-review] LLM API 오류 {e.code}: {err_body}", file=sys.stderr)
        sys.exit(1)
    except Exception as e:
        print(f"[llm-review] LLM 호출 실패: {e}", file=sys.stderr)
        sys.exit(1)


# ── GitLab 코멘트 ──────────────────────────────────────────────

def post_mr_comment(body: str) -> None:
    url = f"{GL_API_URL}/projects/{PROJECT_ID}/merge_requests/{MR_IID}/notes"
    payload = json.dumps({"body": body}).encode()
    req = urllib.request.Request(
        url=url,
        data=payload,
        headers={"Content-Type": "application/json", "PRIVATE-TOKEN": GL_TOKEN},
        method="POST",
    )
    try:
        with urllib.request.urlopen(req, timeout=30) as resp:
            print(f"[llm-review] MR 코멘트 등록 완료 (status={resp.status})")
    except urllib.error.HTTPError as e:
        err_body = e.read().decode(errors="replace")
        print(f"[llm-review] GitLab API 오류 {e.code}: {err_body}", file=sys.stderr)
        sys.exit(1)


def build_comment(
    review: str,
    reasoning: str | None,
    summary: str,
    total_lines: int,
    analyzed_lines: int,
    skipped_count: int,
) -> str:
    thinking_info = " · thinking ✅" if THINKING_ENABLED else ""
    coverage = f"{analyzed_lines:,} / {total_lines:,}줄 분석"
    if skipped_count:
        coverage += f" ({skipped_count}개 파일 예산 초과로 건너뜀)"

    meta = (
        f"> **모델**: `{MODEL}`{thinking_info} | "
        f"**컨텍스트**: ±{CONTEXT_LINES}줄 | {coverage}\n\n"
    )

    body = "## 🤖 LLM 코드 리뷰\n\n" + meta + summary + "\n\n---\n\n" + review

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

    print("[llm-review] diff 파싱 및 우선순위 정렬 중...")
    file_diffs = parse_file_diffs(raw_diff)
    ordered    = prioritize(file_diffs)
    included, skipped = budget_fill(ordered, MAX_LINES)

    total_lines    = sum(f.lines for f in file_diffs)
    analyzed_lines = sum(f.lines for f in included)

    print(
        f"[llm-review] 파일 {len(file_diffs)}개 | "
        f"분석 대상 {len(included)}개 ({analyzed_lines:,}줄) | "
        f"건너뜀 {len(skipped)}개 | "
        f"trivial {sum(1 for f in file_diffs if f.trivial)}개 | "
        f"컨텍스트 ±{CONTEXT_LINES}줄 | "
        f"thinking={THINKING_ENABLED}"
    )
    for fd in ordered:
        tag = "⏭" if fd in skipped else ("💬" if fd.trivial else "✅")
        print(f"  {tag} {fd.path} (+{fd.added}/-{fd.removed}, {fd.lines}줄)")

    if not included:
        print("[llm-review] 분석할 파일 없음 (모두 예산 초과), 스킵")
        return

    diff_for_llm = "".join(f.header + f.content for f in included)
    summary = build_diff_summary(ordered, skipped)

    print(f"\n[llm-review] {MODEL} 에 리뷰 요청 중...")
    review, reasoning = call_llm(diff_for_llm)

    comment = build_comment(
        review=review,
        reasoning=reasoning,
        summary=summary,
        total_lines=total_lines,
        analyzed_lines=analyzed_lines,
        skipped_count=len(skipped),
    )

    print("\n" + "─" * 60)
    print(comment[:2000], "..." if len(comment) > 2000 else "")
    print("─" * 60 + "\n")

    print("[llm-review] GitLab MR 코멘트 등록 중...")
    post_mr_comment(comment)


if __name__ == "__main__":
    main()
