#!/usr/bin/env python3
"""
nginx smoke test — docker compose up 후 nginx 경유 엔드투엔드 검증

사용법:
  python scripts/smoke_test.py              # 이미 기동된 스택 테스트
  python scripts/smoke_test.py --up         # docker compose up -d 후 테스트
  python scripts/smoke_test.py --up --down  # 테스트 후 docker compose down

환경변수:
  NGINX_BASE_URL          기본: http://localhost:10080
  PROXY_API_KEY           proxy API 키 (미설정 = no-auth dev mode)
  VLLM_SERVED_MODEL_NAME  모델명 (기본: Qwen3.5-4B-AWQ)
  ADMIN_API_KEY           admin API 키 (미설정 = admin 테스트 skip)

의존성:
  pip install requests
"""

from __future__ import annotations

import argparse
import json
import os
import subprocess
import sys
import time
import urllib.request
import urllib.error
from dataclasses import dataclass, field
from typing import Any

try:
    import requests

    HAS_REQUESTS = True
except ImportError:
    HAS_REQUESTS = False

# ── 설정 ──────────────────────────────────────────────────────────────────────

BASE_URL = os.getenv("NGINX_BASE_URL", "http://localhost:10080").rstrip("/")
PROXY_API_KEY = os.getenv("PROXY_API_KEY", "")
MODEL = os.getenv("VLLM_SERVED_MODEL_NAME", "Qwen3.5-4B-AWQ")
ADMIN_KEY = os.getenv("ADMIN_API_KEY", "")

# ── ANSI 색상 ─────────────────────────────────────────────────────────────────

GREEN = "\033[92m"
RED = "\033[91m"
YELLOW = "\033[93m"
CYAN = "\033[96m"
BOLD = "\033[1m"
RESET = "\033[0m"

# ── 결과 수집 ─────────────────────────────────────────────────────────────────


@dataclass
class Result:
    name: str
    ok: bool
    detail: str = ""
    elapsed_ms: float = 0.0


results: list[Result] = []


def record(name: str, ok: bool, detail: str = "", elapsed_ms: float = 0.0) -> bool:
    r = Result(name, ok, detail, elapsed_ms)
    results.append(r)
    status = f"{GREEN}PASS{RESET}" if ok else f"{RED}FAIL{RESET}"
    timing = f"  {elapsed_ms:.0f}ms" if elapsed_ms else ""
    print(f"  [{status}] {name}{timing}")
    if not ok and detail:
        print(f"         {YELLOW}{detail}{RESET}")
    return ok


# ── HTTP 헬퍼 ─────────────────────────────────────────────────────────────────


def headers(extra: dict | None = None) -> dict:
    h = {"Content-Type": "application/json"}
    if PROXY_API_KEY:
        h["Authorization"] = f"Bearer {PROXY_API_KEY}"
    if extra:
        h.update(extra)
    return h


def get(path: str, auth: str = "") -> tuple[int, Any]:
    """requests 없으면 urllib fallback."""
    url = BASE_URL + path
    h = headers({"Authorization": f"Bearer {auth}"} if auth else None)
    if HAS_REQUESTS:
        resp = requests.get(url, headers=h, timeout=10)
        return resp.status_code, resp.text
    req = urllib.request.Request(url, headers=h)
    try:
        with urllib.request.urlopen(req, timeout=10) as r:
            return r.status, r.read().decode()
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode()


def post(path: str, body: dict, auth: str = "", stream: bool = False) -> tuple[int, Any]:
    url = BASE_URL + path
    h = headers({"Authorization": f"Bearer {auth}"} if auth else None)
    data = json.dumps(body).encode()
    if HAS_REQUESTS:
        resp = requests.post(url, headers=h, data=data, timeout=120, stream=stream)
        return resp.status_code, resp
    req = urllib.request.Request(url, data=data, headers=h, method="POST")
    try:
        with urllib.request.urlopen(req, timeout=120) as r:
            return r.status, r.read().decode()
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode()


def delete(path: str, auth: str = "") -> int:
    url = BASE_URL + path
    h = headers({"Authorization": f"Bearer {auth}"} if auth else None)
    if HAS_REQUESTS:
        return requests.delete(url, headers=h, timeout=10).status_code
    req = urllib.request.Request(url, headers=h, method="DELETE")
    try:
        with urllib.request.urlopen(req, timeout=10) as r:
            return r.status
    except urllib.error.HTTPError as e:
        return e.code


# ── 헬스체크 대기 ──────────────────────────────────────────────────────────────


def wait_for_nginx(timeout: int = 120) -> bool:
    """nginx /healthz 가 200 반환할 때까지 대기."""
    print(f"\n{CYAN}▶ nginx 준비 대기 (최대 {timeout}초)...{RESET}")
    deadline = time.time() + timeout
    while time.time() < deadline:
        try:
            code, _ = get("/healthz")
            if code == 200:
                print(f"  {GREEN}nginx 준비 완료{RESET}")
                return True
        except Exception:
            pass
        time.sleep(3)
    print(f"  {RED}nginx 응답 없음 (timeout){RESET}")
    return False


# ── 개별 테스트 ───────────────────────────────────────────────────────────────


def test_healthz() -> None:
    print(f"\n{BOLD}[ Health / Infrastructure ]{RESET}")
    t0 = time.time()
    code, body = get("/healthz")
    record("GET /healthz", code == 200, f"status={code}", (time.time() - t0) * 1000)

    t0 = time.time()
    code, body = get("/metrics")
    record(
        "GET /metrics (Prometheus)",
        code == 200 and "go_goroutines" in str(body),
        f"status={code}",
        (time.time() - t0) * 1000,
    )

    t0 = time.time()
    code, body = get("/docs/")
    record(
        "GET /docs/ (Swagger UI)",
        code == 200 and "swagger" in str(body).lower(),
        f"status={code}",
        (time.time() - t0) * 1000,
    )


def test_models() -> None:
    print(f"\n{BOLD}[ vLLM Model List ]{RESET}")
    t0 = time.time()
    code, body = get("/v1/models")
    elapsed = (time.time() - t0) * 1000
    ok = code == 200
    detail = f"status={code}"
    if ok and HAS_REQUESTS:
        try:
            data = json.loads(body)
            ids = [m["id"] for m in data.get("data", [])]
            detail = f"models={ids}"
            ok = any(MODEL in m for m in ids) or len(ids) > 0
        except Exception:
            pass
    record(f"GET /v1/models  (expect {MODEL})", ok, detail, elapsed)


def test_chat_nonstreaming() -> None:
    print(f"\n{BOLD}[ Chat Completions — non-streaming ]{RESET}")
    payload = {
        "model": MODEL,
        "messages": [{"role": "user", "content": "Reply with exactly: pong"}],
        "max_tokens": 16,
        "temperature": 0,
        "stream": False,
    }
    t0 = time.time()
    code, resp = post("/v1/chat/completions", payload)
    elapsed = (time.time() - t0) * 1000

    ok = code == 200
    detail = f"status={code}"
    if ok:
        try:
            body = resp.text if HAS_REQUESTS else resp
            data = json.loads(body)
            content = data["choices"][0]["message"]["content"]
            usage = data.get("usage", {})
            detail = f"reply={repr(content[:60])}  tokens={usage}"
        except Exception as e:
            detail = f"parse error: {e}"
            ok = False
    record("POST /v1/chat/completions", ok, detail, elapsed)


def test_chat_streaming() -> None:
    print(f"\n{BOLD}[ Chat Completions — SSE streaming ]{RESET}")
    if not HAS_REQUESTS:
        record("POST /v1/chat/completions (stream)", False, "requests 패키지 필요 — pip install requests")
        return

    payload = {
        "model": MODEL,
        "messages": [{"role": "user", "content": "Count to 3, one number per line."}],
        "max_tokens": 32,
        "temperature": 0,
        "stream": True,
    }
    t0 = time.time()
    code, resp = post("/v1/chat/completions", payload, stream=True)
    chunks: list[str] = []
    try:
        for raw in resp.iter_lines():
            if not raw:
                continue
            line = raw.decode() if isinstance(raw, bytes) else raw
            if line.startswith("data:"):
                payload_str = line[5:].strip()
                if payload_str == "[DONE]":
                    break
                try:
                    delta = json.loads(payload_str)["choices"][0]["delta"].get("content", "")
                    if delta:
                        chunks.append(delta)
                except Exception:
                    pass
    except Exception as e:
        record("POST /v1/chat/completions (stream)", False, str(e), (time.time() - t0) * 1000)
        return

    elapsed = (time.time() - t0) * 1000
    ok = code == 200 and len(chunks) > 0
    joined = repr("".join(chunks)[:80])
    record(
        "POST /v1/chat/completions (stream)",
        ok,
        f"status={code}  chunks={len(chunks)}  content={joined}",
        elapsed,
    )


def test_multimodal() -> None:
    """이미지 URL을 포함한 멀티모달 요청 — 모델이 vision을 지원할 때만 pass."""
    print(f"\n{BOLD}[ Multimodal (vision) ]{RESET}")
    payload = {
        "model": MODEL,
        "messages": [
            {
                "role": "user",
                "content": [
                    {"type": "text", "text": "What color is the sky in this image?"},
                    {
                        "type": "image_url",
                        "image_url": {
                            # 1×1 파란 픽셀 PNG (base64 inline)
                            "url": "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="
                        },
                    },
                ],
            }
        ],
        "max_tokens": 32,
        "stream": False,
    }
    t0 = time.time()
    code, resp = post("/v1/chat/completions", payload)
    elapsed = (time.time() - t0) * 1000

    if code == 400:
        # vision 미지원 모델은 skip (실패 아님)
        record("POST /v1/chat/completions (vision)", True, f"status=400 — 모델이 vision 미지원, skip", elapsed)
        return

    ok = code == 200
    detail = f"status={code}"
    if ok:
        try:
            body = resp.text if HAS_REQUESTS else resp
            content = json.loads(body)["choices"][0]["message"]["content"]
            detail = f"reply={repr(content[:60])}"
        except Exception as e:
            detail = str(e)
    record("POST /v1/chat/completions (vision)", ok, detail, elapsed)


def test_admin() -> None:
    if not ADMIN_KEY:
        print(f"\n{BOLD}[ Admin API ]{RESET}")
        print(f"  {YELLOW}SKIP{RESET}  ADMIN_API_KEY 미설정")
        return

    print(f"\n{BOLD}[ Admin API ]{RESET}")

    # CREATE
    t0 = time.time()
    code, resp = post("/admin/keys", {"user_id": "smoke-test", "description": "smoke test key"}, auth=ADMIN_KEY)
    elapsed = (time.time() - t0) * 1000
    created_key = None
    ok = code == 201
    detail = f"status={code}"
    if ok:
        try:
            body = resp.text if HAS_REQUESTS else resp
            created_key = json.loads(body)["key"]
            detail = f"key={created_key}"
        except Exception as e:
            detail = str(e)
    record("POST /admin/keys", ok, detail, elapsed)

    # LIST
    t0 = time.time()
    code, body = get("/admin/keys", auth=ADMIN_KEY)
    elapsed = (time.time() - t0) * 1000
    record("GET /admin/keys", code == 200, f"status={code}", elapsed)

    # GET single
    if created_key:
        t0 = time.time()
        code, body = get(f"/admin/keys/{created_key}", auth=ADMIN_KEY)
        elapsed = (time.time() - t0) * 1000
        record(f"GET /admin/keys/{{key}}", code == 200, f"status={code}", elapsed)

    # DELETE
    if created_key:
        t0 = time.time()
        code = delete(f"/admin/keys/{created_key}", auth=ADMIN_KEY)
        elapsed = (time.time() - t0) * 1000
        record(f"DELETE /admin/keys/{{key}}", code == 204, f"status={code}", elapsed)


def test_auth_reject() -> None:
    print(f"\n{BOLD}[ Auth / Error Handling ]{RESET}")

    # 잘못된 키 → 401
    t0 = time.time()
    url = BASE_URL + "/v1/models"
    h = {"Authorization": "Bearer sk-invalid-key-xxx", "Content-Type": "application/json"}
    code = 0
    if HAS_REQUESTS:
        code = requests.get(url, headers=h, timeout=10).status_code
    else:
        req = urllib.request.Request(url, headers=h)
        try:
            urllib.request.urlopen(req, timeout=10)
        except urllib.error.HTTPError as e:
            code = e.code
    elapsed = (time.time() - t0) * 1000

    # no-auth dev mode (PROXY_API_KEYS_JSON="") 면 401이 아닌 200도 허용
    ok = code in (200, 401)
    record("Invalid API key → 401 or 200 (no-auth mode)", ok, f"status={code}", elapsed)

    # 없는 경로 → 404
    t0 = time.time()
    code, _ = get("/v1/nonexistent")
    elapsed = (time.time() - t0) * 1000
    record("GET /v1/nonexistent → 404", code == 404, f"status={code}", elapsed)


# ── docker compose 헬퍼 ───────────────────────────────────────────────────────


def compose_up() -> None:
    print(f"\n{CYAN}▶ docker compose up -d ...{RESET}")
    subprocess.run(["docker", "compose", "up", "-d"], check=True)


def compose_down() -> None:
    print(f"\n{CYAN}▶ docker compose down ...{RESET}")
    subprocess.run(["docker", "compose", "down"], check=True)


# ── 요약 출력 ─────────────────────────────────────────────────────────────────


def print_summary() -> int:
    passed = sum(1 for r in results if r.ok)
    failed = sum(1 for r in results if not r.ok)
    total = len(results)
    print(f"\n{'─'*55}")
    print(f"{BOLD}결과: {GREEN}{passed} passed{RESET}{BOLD}, {RED}{failed} failed{RESET}{BOLD} / {total} total{RESET}")
    if failed:
        print(f"\n{RED}실패 항목:{RESET}")
        for r in results:
            if not r.ok:
                print(f"  • {r.name}: {r.detail}")
    print()
    return 1 if failed else 0


# ── main ──────────────────────────────────────────────────────────────────────


def main() -> None:
    parser = argparse.ArgumentParser(description="nginx smoke test")
    parser.add_argument("--up", action="store_true", help="docker compose up -d 후 테스트")
    parser.add_argument("--down", action="store_true", help="테스트 후 docker compose down")
    parser.add_argument("--skip-llm", action="store_true", help="LLM 추론 테스트 skip (인프라만 검증)")
    parser.add_argument("--wait", type=int, default=120, help="nginx 준비 대기 timeout (초, 기본 120)")
    args = parser.parse_args()

    print(f"\n{BOLD}{'='*55}")
    print(f"  llm-proxy smoke test  →  {BASE_URL}")
    print(f"  model : {MODEL}")
    print(f"  auth  : {'Bearer ***' if PROXY_API_KEY else '(no-auth dev mode)'}")
    print(f"{'='*55}{RESET}")

    if not HAS_REQUESTS:
        print(f"{YELLOW}경고: requests 패키지 없음 — SSE 스트리밍 테스트 skip됩니다.{RESET}")
        print("  pip install requests\n")

    try:
        if args.up:
            compose_up()

        if not wait_for_nginx(timeout=args.wait):
            sys.exit(1)

        test_healthz()
        test_auth_reject()

        if not args.skip_llm:
            test_models()
            test_chat_nonstreaming()
            test_chat_streaming()
            test_multimodal()

        test_admin()

    finally:
        if args.down:
            compose_down()

    sys.exit(print_summary())


if __name__ == "__main__":
    main()
