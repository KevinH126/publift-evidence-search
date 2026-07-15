"""Tests for the embedding sidecar FastAPI app."""

import numpy as np
import pytest
from fastapi.testclient import TestClient

import main


class _FakeModel:
    def encode(self, texts, normalize_embeddings=True, show_progress_bar=False):
        return np.array([[float(i)] * 4 for i in range(len(texts))])


@pytest.fixture(autouse=True)
def fake_model(monkeypatch):
    monkeypatch.setattr(main, "model", _FakeModel())


@pytest.fixture
def client():
    return TestClient(main.app)


def test_health(client):
    resp = client.get("/health")
    assert resp.status_code == 200
    assert resp.json() == {"status": "ok", "model_loaded": True}


def test_embed_basic(client):
    resp = client.post("/embed", json={"texts": ["hello", "world"]})
    assert resp.status_code == 200
    body = resp.json()
    assert body["dimension"] == 4
    assert len(body["embeddings"]) == 2


def test_embed_empty_texts_rejected(client):
    resp = client.post("/embed", json={"texts": []})
    assert resp.status_code == 400


def test_embed_too_many_texts_rejected(client):
    resp = client.post("/embed", json={"texts": ["x"] * 257})
    assert resp.status_code == 400


def test_embed_max_texts_allowed(client):
    resp = client.post("/embed", json={"texts": ["x"] * 256})
    assert resp.status_code == 200
