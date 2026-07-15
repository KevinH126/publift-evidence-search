#!/usr/bin/env python3
"""Upload the corpus produced by fetch_pubmed_corpus.py to a running PubLift API.

Reads pubmed_corpus/manifest.json and POSTs each .txt file to
POST /api/v1/studies (multipart/form-data), with title/authors/journal/year/
doi/study_type/topic passed as separate form fields — matching how
internal/api/router.go's uploadStudy handler expects them (authors joined
with ';' per splitList's parsing rule).

Usage:
    python scripts/upload_corpus.py                  # uses http://localhost:8080
    API_URL=http://localhost:9000 python scripts/upload_corpus.py
"""

import json
import mimetypes
import os
import sys
import time
import urllib.error
import urllib.request
import uuid

# Windows consoles often default stdout to cp1252, which can't encode titles
# containing e.g. Greek letters or special punctuation. Force UTF-8 output.
sys.stdout.reconfigure(encoding="utf-8", errors="replace")

API_URL = os.environ.get("API_URL", "http://localhost:8080").rstrip("/")
CORPUS_DIR = os.path.join(os.path.dirname(__file__), "..", "pubmed_corpus")
MANIFEST_PATH = os.path.join(CORPUS_DIR, "manifest.json")

# The API rate-limits at 60 req/min per IP (internal/store/redis.go). All uploads
# come from this one machine's IP, so stay comfortably under that: ~1.1s/request.
DELAY_BETWEEN_UPLOADS = 1.1
MAX_RETRIES_ON_429 = 5


def build_multipart_body(fields, file_field_name, file_path):
    boundary = uuid.uuid4().hex
    parts = []

    for name, value in fields.items():
        if value is None or value == "":
            continue
        parts.append(
            f"--{boundary}\r\n"
            f'Content-Disposition: form-data; name="{name}"\r\n\r\n'
            f"{value}\r\n".encode("utf-8")
        )

    filename = os.path.basename(file_path)
    content_type = mimetypes.guess_type(filename)[0] or "text/plain"
    with open(file_path, "rb") as f:
        file_data = f.read()

    parts.append(
        (
            f"--{boundary}\r\n"
            f'Content-Disposition: form-data; name="{file_field_name}"; filename="{filename}"\r\n'
            f"Content-Type: {content_type}\r\n\r\n"
        ).encode("utf-8")
        + file_data
        + b"\r\n"
    )
    parts.append(f"--{boundary}--\r\n".encode("utf-8"))

    body = b"".join(
        p if isinstance(p, bytes) else p.encode("utf-8") for p in parts
    )
    return body, boundary


def upload_study(record):
    file_path = os.path.join(CORPUS_DIR, record["file"])
    fields = {
        "title": record.get("title", ""),
        "authors": ";".join(record.get("authors", [])),
        "journal": record.get("journal", ""),
        "year": record.get("year", ""),
        "doi": record.get("doi", ""),
        "study_type": record.get("study_type", ""),
        "topic": record.get("topic", ""),
    }
    body, boundary = build_multipart_body(fields, "file", file_path)

    req = urllib.request.Request(
        f"{API_URL}/api/v1/studies",
        data=body,
        method="POST",
        headers={"Content-Type": f"multipart/form-data; boundary={boundary}"},
    )
    with urllib.request.urlopen(req) as resp:
        return resp.status, json.load(resp)


def main():
    if not os.path.exists(MANIFEST_PATH):
        print(f"No manifest found at {MANIFEST_PATH}.")
        print("Run scripts/fetch_pubmed_corpus.py first.")
        sys.exit(1)

    with open(MANIFEST_PATH, "r", encoding="utf-8") as f:
        manifest = json.load(f)

    print(f"Uploading {len(manifest)} studies to {API_URL} ...")

    ok, failed = 0, 0
    for i, record in enumerate(manifest, 1):
        for attempt in range(1, MAX_RETRIES_ON_429 + 1):
            try:
                status, _ = upload_study(record)
                ok += 1
                print(f"[{i}/{len(manifest)}] {status} - {record['title'][:70]}")
                break
            except urllib.error.HTTPError as e:
                if e.code == 429 and attempt < MAX_RETRIES_ON_429:
                    wait = 5 * attempt
                    print(f"[{i}/{len(manifest)}] rate limited, retrying in {wait}s...")
                    time.sleep(wait)
                    continue
                failed += 1
                detail = e.read().decode("utf-8", errors="replace")
                print(f"[{i}/{len(manifest)}] FAILED ({e.code}) - {record['title'][:70]}")
                print(f"    {detail[:200]}")
                break
            except Exception as e:
                failed += 1
                print(f"[{i}/{len(manifest)}] FAILED - {record['title'][:70]}: {e}")
                break
        time.sleep(DELAY_BETWEEN_UPLOADS)

    print(f"\nDone. {ok} uploaded, {failed} failed.")


if __name__ == "__main__":
    main()
