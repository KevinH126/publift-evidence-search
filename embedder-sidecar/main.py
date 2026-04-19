"""
Embedding sidecar — the entire Python component of the project.
Loads a sentence-transformers model once on startup,
exposes a single POST /embed endpoint.
"""

import os
from contextlib import asynccontextmanager

import numpy as np
from fastapi import FastAPI, HTTPException
from pydantic import BaseModel
from sentence_transformers import SentenceTransformer

model: SentenceTransformer | None = None


@asynccontextmanager
async def lifespan(app: FastAPI):
    """Load model on startup, free on shutdown."""
    global model
    model_name = os.getenv("MODEL_NAME", "sentence-transformers/all-MiniLM-L6-v2")
    print(f"Loading model: {model_name}")
    model = SentenceTransformer(model_name)
    print(f"Model loaded. Embedding dimension: {model.get_sentence_embedding_dimension()}")
    yield
    model = None


app = FastAPI(title="Embedding Sidecar", lifespan=lifespan)


class EmbedRequest(BaseModel):
    texts: list[str]


class EmbedResponse(BaseModel):
    embeddings: list[list[float]]
    dimension: int


@app.post("/embed", response_model=EmbedResponse)
async def embed(req: EmbedRequest):
    if not req.texts:
        raise HTTPException(status_code=400, detail="texts list cannot be empty")
    if len(req.texts) > 256:
        raise HTTPException(status_code=400, detail="max 256 texts per batch")

    vectors = model.encode(req.texts, normalize_embeddings=True, show_progress_bar=False)
    return EmbedResponse(
        embeddings=vectors.tolist(),
        dimension=vectors.shape[1],
    )


@app.get("/health")
async def health():
    return {"status": "ok", "model_loaded": model is not None}
