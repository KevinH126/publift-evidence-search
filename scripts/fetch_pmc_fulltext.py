#!/usr/bin/env python3
"""Upgrade abstract-only studies from fetch_pubmed_corpus.py to full text, for
whichever ones are available in PMC's Open Access Subset.

Not every PubMed article has a PMC full-text copy, and not every PMC copy is
in the OA subset (most journal PDFs are copyrighted, even when the abstract
is freely indexed) — so this only upgrades a fraction of your corpus. Studies
that don't resolve stay as abstract-only, which is a perfectly fine fallback.

Pipeline per study, using only official NCBI services (no scraping):
  1. PMID -> PMCID, via the PMC ID Converter API (batches of up to 200 IDs)
  2. PMCID -> OA download link, via the PMC OA Web Service (oa.fcgi)
     (this step is what tells you whether the article is actually OA-licensed —
     having a PMCID does NOT imply the full text is open access)
  3. Download the .tar.gz package over FTP, extract the .nxml, pull out the
     <body> paragraph text, and prepend it after the existing abstract.

Run this AFTER fetch_pubmed_corpus.py and BEFORE upload_corpus.py.

Usage:
    python scripts/fetch_pmc_fulltext.py
"""

import io
import json
import os
import sys
import tarfile
import time
import urllib.error
import urllib.parse
import urllib.request
import xml.etree.ElementTree as ET

# Windows consoles often default stdout to cp1252, which can't encode titles
# containing e.g. Greek letters or special punctuation. Force UTF-8 output.
sys.stdout.reconfigure(encoding="utf-8", errors="replace")

IDCONV_URL = "https://pmc.ncbi.nlm.nih.gov/tools/idconv/api/v1/articles/"
OA_URL = "https://www.ncbi.nlm.nih.gov/pmc/utils/oa/oa.fcgi"

# NCBI asks API callers to identify themselves via tool+email. Override via env
# vars if you'd rather not use the default.
TOOL_NAME = os.environ.get("NCBI_TOOL_NAME", "publift-corpus-builder")
TOOL_EMAIL = os.environ.get("NCBI_TOOL_EMAIL", "hartk2006@gmail.com")

IDCONV_BATCH_SIZE = 190  # API cap is 200; leave headroom
REQUEST_DELAY = 0.34     # polite pacing, matches E-utilities' 3 req/sec guidance

CORPUS_DIR = os.path.join(os.path.dirname(__file__), "..", "pubmed_corpus")
MANIFEST_PATH = os.path.join(CORPUS_DIR, "manifest.json")


def chunked(seq, size):
    for i in range(0, len(seq), size):
        yield seq[i : i + size]


def pmid_to_pmcid(pmids):
    """Returns {pmid: pmcid} for whichever PMIDs have a PMC copy at all."""
    result = {}
    for batch in chunked(pmids, IDCONV_BATCH_SIZE):
        params = {
            "tool": TOOL_NAME,
            "email": TOOL_EMAIL,
            "ids": ",".join(batch),
            "format": "json",
        }
        url = f"{IDCONV_URL}?{urllib.parse.urlencode(params)}"
        try:
            with urllib.request.urlopen(url) as resp:
                data = json.load(resp)
        except Exception as e:
            print(f"  !! id-converter batch failed: {e}")
            time.sleep(REQUEST_DELAY)
            continue

        for rec in data.get("records", []):
            pmcid = rec.get("pmcid")
            pmid = str(rec.get("pmid") or rec.get("requested-id") or "")
            if pmcid and pmid:
                result[pmid] = pmcid
        time.sleep(REQUEST_DELAY)
    return result


def get_oa_download_url(pmcid):
    """Returns the ftp:// tgz URL for a PMCID if it's in the OA subset, else None."""
    params = {"id": pmcid}
    url = f"{OA_URL}?{urllib.parse.urlencode(params)}"
    try:
        with urllib.request.urlopen(url) as resp:
            xml_bytes = resp.read()
    except Exception as e:
        print(f"  !! oa.fcgi request failed for {pmcid}: {e}")
        return None

    try:
        root = ET.fromstring(xml_bytes)
    except ET.ParseError:
        return None

    if root.find("error") is not None:
        return None  # not in the OA subset, or invalid id

    for record in root.findall(".//record"):
        for link in record.findall("link"):
            if link.get("format") == "tgz":
                return link.get("href")
    return None


def resolve_download_url(ftp_url):
    """oa.fcgi still returns links under the legacy ftp:// tree, which NCBI has
    moved to pub/pmc/deprecated/ (removal planned August 2026 per
    https://ftp.ncbi.nlm.nih.gov/pub/pmc/readme.txt). Rewrite to the path that
    actually resolves today: same host, https scheme, under /pub/pmc/deprecated/.
    """
    path = ftp_url.split("ftp://ftp.ncbi.nlm.nih.gov", 1)[-1]
    path = path.replace("/pub/pmc/", "/pub/pmc/deprecated/", 1)
    return "https://ftp.ncbi.nlm.nih.gov" + path


def download_and_extract_body_text(ftp_url):
    """Downloads the OA tar.gz and returns the article body's plain text."""
    url = resolve_download_url(ftp_url)
    with urllib.request.urlopen(url, timeout=60) as resp:
        raw = resp.read()

    with tarfile.open(fileobj=io.BytesIO(raw), mode="r:gz") as tar:
        nxml_member = next(
            (m for m in tar.getmembers() if m.name.endswith(".nxml")), None
        )
        if nxml_member is None:
            return ""
        nxml_bytes = tar.extractfile(nxml_member).read()

    try:
        root = ET.fromstring(nxml_bytes)
    except ET.ParseError:
        return ""

    body = root.find(".//body")
    if body is None:
        return ""

    paragraphs = []
    for p in body.findall(".//p"):
        text = "".join(p.itertext()).strip()
        if text:
            paragraphs.append(text)
    return "\n\n".join(paragraphs)


def main():
    if not os.path.exists(MANIFEST_PATH):
        print(f"No manifest found at {MANIFEST_PATH}.")
        print("Run scripts/fetch_pubmed_corpus.py first.")
        sys.exit(1)

    with open(MANIFEST_PATH, "r", encoding="utf-8") as f:
        manifest = json.load(f)

    pmids = [r["pmid"] for r in manifest if r.get("pmid")]
    print(f"Resolving PMC IDs for {len(pmids)} studies...")
    pmid_map = pmid_to_pmcid(pmids)
    print(f"  -> {len(pmid_map)} have a PMC copy (not all will be Open Access)")

    upgraded, no_pmc, no_oa, failed = 0, 0, 0, 0

    for i, record in enumerate(manifest, 1):
        pmid = record.get("pmid")
        pmcid = pmid_map.get(pmid)
        if not pmcid:
            no_pmc += 1
            continue

        ftp_url = get_oa_download_url(pmcid)
        time.sleep(REQUEST_DELAY)
        if not ftp_url:
            no_oa += 1
            continue

        try:
            body_text = download_and_extract_body_text(ftp_url)
        except Exception as e:
            print(f"[{i}/{len(manifest)}] FAILED to fetch/extract {pmcid}: {e}")
            failed += 1
            continue

        if not body_text:
            no_oa += 1
            continue

        file_path = os.path.join(CORPUS_DIR, record["file"])
        with open(file_path, "r", encoding="utf-8") as f:
            abstract = f.read().strip()
        with open(file_path, "w", encoding="utf-8") as f:
            f.write(abstract + "\n\n" + body_text)

        record["pmcid"] = pmcid
        record["fulltext"] = True
        upgraded += 1
        print(f"[{i}/{len(manifest)}] upgraded to full text - {record['title'][:60]}")
        time.sleep(REQUEST_DELAY)

    with open(MANIFEST_PATH, "w", encoding="utf-8") as f:
        json.dump(manifest, f, indent=2)

    print(
        f"\nDone. {upgraded} upgraded to full text, "
        f"{no_pmc} not in PMC at all, {no_oa} in PMC but not Open Access, "
        f"{failed} failed to fetch."
    )
    print("Abstract-only studies were left unchanged — no data was removed.")


if __name__ == "__main__":
    main()
