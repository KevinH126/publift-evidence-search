#!/usr/bin/env python3
"""Fetch PubMed abstracts for exercise-science topics and save them as plain-text
files ready to upload to PubLift, plus a manifest.json of metadata for each one.

Uses NCBI's E-utilities (esearch + efetch) — no scraping, no PDFs involved.
Set NCBI_API_KEY in the environment to raise the rate limit from 3 req/sec to 10/sec:
https://www.ncbi.nlm.nih.gov/account/settings/ (free account, under API Key Management)

Usage:
    python scripts/fetch_pubmed_corpus.py
"""

import json
import os
import re
import sys
import time
import urllib.parse
import urllib.request
import xml.etree.ElementTree as ET

# Windows consoles often default stdout to cp1252, which can't encode titles
# containing e.g. Greek letters or special punctuation. Force UTF-8 output.
sys.stdout.reconfigure(encoding="utf-8", errors="replace")

ESEARCH_URL = "https://eutils.ncbi.nlm.nih.gov/entrez/eutils/esearch.fcgi"
EFETCH_URL = "https://eutils.ncbi.nlm.nih.gov/entrez/eutils/efetch.fcgi"

API_KEY = os.environ.get("NCBI_API_KEY", "")
RATE_LIMIT_DELAY = 0.11 if API_KEY else 0.34  # stay under 10/sec or 3/sec

ABSTRACTS_PER_QUERY = 30
OUTPUT_DIR = os.path.join(os.path.dirname(__file__), "..", "pubmed_corpus")

# topic (matches PubLift's free-text `topic` field) -> list of PubMed search queries.
# Multiple queries per topic increase recall/diversity; duplicates are deduped by PMID.
TOPICS = {
    "hypertrophy": [
        "resistance training hypertrophy",
        "muscle protein synthesis resistance exercise",
        "training volume hypertrophy",
        "time under tension muscle growth",
        "mechanical tension muscle hypertrophy",
    ],
    "strength": [
        "progressive overload strength training",
        "one repetition maximum strength training",
        "neural adaptations strength training",
        "powerlifting performance training",
        "maximal strength training program",
    ],
    "rep-ranges": [
        "repetition range muscle hypertrophy",
        "low load high repetition resistance training",
        "high load low repetition resistance training",
        "training to failure resistance exercise",
    ],
    "nutrition": [
        "protein intake muscle hypertrophy",
        "protein timing resistance training",
        "leucine threshold muscle protein synthesis",
        "caloric surplus muscle gain",
        "caloric deficit resistance training lean mass",
    ],
    "supplementation": [
        "creatine supplementation strength performance",
        "beta-alanine supplementation exercise performance",
        "caffeine supplementation resistance exercise",
        "branched chain amino acids exercise recovery",
    ],
    "recovery": [
        "delayed onset muscle soreness recovery",
        "recovery resistance training frequency",
        "sleep muscle recovery resistance training",
        "active recovery exercise performance",
    ],
    "programming": [
        "periodization resistance training",
        "training frequency muscle hypertrophy",
        "autoregulation resistance training",
        "deload periodization strength training",
        "concurrent training interference effect",
    ],
    "biomechanics": [
        "range of motion resistance training hypertrophy",
        "eccentric training muscle damage",
        "squat biomechanics knee",
        "deadlift biomechanics lumbar spine",
    ],
    "injury-prevention": [
        "resistance training injury prevention",
        "tendon adaptation resistance training",
        "joint health resistance training",
    ],
    "hormones": [
        "testosterone resistance training adaptation",
        "growth hormone resistance exercise",
        "cortisol resistance training stress",
    ],
    "aging": [
        "resistance training older adults sarcopenia",
        "sarcopenia resistance exercise intervention",
    ],
    "sex-differences": [
        "sex differences resistance training adaptation",
        "resistance training women muscle hypertrophy",
    ],
    "cardio": [
        "concurrent aerobic resistance training interference",
        "cardiovascular exercise muscle strength",
    ],
    "body-composition": [
        "body composition resistance training",
        "fat loss resistance training diet",
    ],
}

# PubMed PublicationType -> PubLift study_type enum. Anything unmatched -> "unknown".
PUB_TYPE_MAP = {
    "meta-analysis": "meta-analysis",
    "systematic review": "systematic-review",
    "randomized controlled trial": "rct",
    "observational study": "observational",
    "review": "review",
    "case reports": "case-study",
}
# priority order when an article has multiple publication types
PUB_TYPE_PRIORITY = [
    "meta-analysis",
    "systematic-review",
    "rct",
    "observational",
    "review",
    "case-study",
]


def esearch(query, retmax):
    params = {
        "db": "pubmed",
        "term": query,
        "retmax": retmax,
        "retmode": "json",
        "sort": "relevance",
    }
    if API_KEY:
        params["api_key"] = API_KEY
    url = f"{ESEARCH_URL}?{urllib.parse.urlencode(params)}"
    with urllib.request.urlopen(url) as resp:
        data = json.load(resp)
    return data.get("esearchresult", {}).get("idlist", [])


def efetch_batch(pmids):
    params = {"db": "pubmed", "id": ",".join(pmids), "retmode": "xml"}
    if API_KEY:
        params["api_key"] = API_KEY
    url = f"{EFETCH_URL}?{urllib.parse.urlencode(params)}"
    with urllib.request.urlopen(url) as resp:
        return resp.read()


def pick_study_type(pub_types):
    lowered = {t.strip().lower() for t in pub_types}
    mapped = {PUB_TYPE_MAP[t] for t in lowered if t in PUB_TYPE_MAP}
    for candidate in PUB_TYPE_PRIORITY:
        if candidate in mapped:
            return candidate
    return "unknown"


def parse_articles(xml_bytes):
    root = ET.fromstring(xml_bytes)
    articles = []
    for art in root.findall(".//PubmedArticle"):
        medline = art.find("MedlineCitation")
        if medline is None:
            continue
        article_el = medline.find("Article")
        if article_el is None:
            continue

        pmid = medline.findtext("PMID", "").strip()

        abstract_parts = article_el.findall(".//Abstract/AbstractText")
        abstract = " ".join((a.text or "").strip() for a in abstract_parts).strip()
        if not abstract:
            continue  # skip studies with no abstract text — nothing useful to embed

        title = (article_el.findtext("ArticleTitle") or "").strip()

        journal = (article_el.findtext("Journal/Title") or "").strip()

        year = ""
        pub_date = article_el.find("Journal/JournalIssue/PubDate")
        if pub_date is not None:
            year = (pub_date.findtext("Year") or "").strip()
            if not year:
                medline_date = pub_date.findtext("MedlineDate") or ""
                match = re.search(r"\d{4}", medline_date)
                year = match.group(0) if match else ""

        authors = []
        author_list = article_el.find("AuthorList")
        if author_list is not None:
            for a in author_list.findall("Author"):
                last = a.findtext("LastName")
                fore = a.findtext("ForeName")
                if last:
                    authors.append(f"{fore} {last}".strip() if fore else last)

        doi = ""
        for eid in art.findall(".//ArticleId"):
            if eid.get("IdType") == "doi":
                doi = (eid.text or "").strip()
                break

        pub_types = [
            pt.text or ""
            for pt in article_el.findall("PublicationTypeList/PublicationType")
        ]
        study_type = pick_study_type(pub_types)

        articles.append(
            {
                "pmid": pmid,
                "title": title,
                "abstract": abstract,
                "journal": journal,
                "year": year,
                "authors": authors,
                "doi": doi,
                "study_type": study_type,
            }
        )
    return articles


def slugify(text, maxlen=60):
    keep = re.sub(r"[^a-z0-9]+", "-", text.lower()).strip("-")
    return keep[:maxlen] or "untitled"


def main():
    os.makedirs(OUTPUT_DIR, exist_ok=True)
    seen_pmids = set()
    manifest = []

    for topic, queries in TOPICS.items():
        topic_dir = os.path.join(OUTPUT_DIR, topic)
        os.makedirs(topic_dir, exist_ok=True)

        for query in queries:
            print(f"[{topic}] searching: {query}")
            try:
                pmids = esearch(query, ABSTRACTS_PER_QUERY)
            except Exception as e:
                print(f"  !! esearch failed: {e}")
                continue
            time.sleep(RATE_LIMIT_DELAY)

            new_pmids = [p for p in pmids if p not in seen_pmids]
            if not new_pmids:
                print("  -> no new results")
                continue

            try:
                xml_bytes = efetch_batch(new_pmids)
            except Exception as e:
                print(f"  !! efetch failed: {e}")
                continue
            time.sleep(RATE_LIMIT_DELAY)

            articles = parse_articles(xml_bytes)
            for article in articles:
                seen_pmids.add(article["pmid"])
                fname = f"{article['pmid']}-{slugify(article['title'])}.txt"
                path = os.path.join(topic_dir, fname)
                with open(path, "w", encoding="utf-8") as f:
                    f.write(article["abstract"])

                manifest.append(
                    {
                        "file": os.path.relpath(path, OUTPUT_DIR),
                        "topic": topic,
                        "title": article["title"],
                        "authors": article["authors"],
                        "journal": article["journal"],
                        "year": article["year"],
                        "doi": article["doi"],
                        "study_type": article["study_type"],
                        "pmid": article["pmid"],
                    }
                )
            print(f"  -> saved {len(articles)} abstracts")

    manifest_path = os.path.join(OUTPUT_DIR, "manifest.json")
    with open(manifest_path, "w", encoding="utf-8") as f:
        json.dump(manifest, f, indent=2)

    print(f"\nDone. {len(seen_pmids)} unique studies saved to {OUTPUT_DIR}/")
    print(f"Manifest: {manifest_path}")
    print("Next: python scripts/upload_corpus.py")


if __name__ == "__main__":
    main()
