-- migrate.sql
-- Run once on database initialization

-- Enable pgvector extension
CREATE EXTENSION IF NOT EXISTS vector;

-- Studies table (an ingested research paper + its bibliographic/evidence metadata)
CREATE TABLE IF NOT EXISTS studies (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    title       TEXT NOT NULL,
    authors     TEXT[] NOT NULL DEFAULT '{}',
    journal     TEXT NOT NULL DEFAULT '',
    year        INTEGER,
    doi         TEXT NOT NULL DEFAULT '',
    study_type  TEXT NOT NULL DEFAULT 'unknown',  -- meta-analysis | systematic-review | rct | cohort | observational | review | case-study | unknown
    topic       TEXT NOT NULL DEFAULT '',         -- hypertrophy | strength | nutrition | recovery | programming | ...
    population  TEXT NOT NULL DEFAULT '',
    sample_size INTEGER,
    filename    TEXT NOT NULL,
    file_type   TEXT NOT NULL,            -- "pdf", "txt", "md"
    file_size   BIGINT NOT NULL,
    status      TEXT NOT NULL DEFAULT 'pending',  -- pending | processing | completed | failed
    error_msg   TEXT,
    chunk_count INTEGER DEFAULT 0,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Chunks table with vector embeddings
CREATE TABLE IF NOT EXISTS chunks (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    study_id    UUID NOT NULL REFERENCES studies(id) ON DELETE CASCADE,
    content     TEXT NOT NULL,
    embedding   vector(384),              -- MiniLM-L6-v2 output dimension
    page_num    INTEGER,
    chunk_index INTEGER NOT NULL,
    token_count INTEGER,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- HNSW index for fast cosine similarity search
-- m=16, ef_construction=200 are good defaults for <1M vectors
CREATE INDEX IF NOT EXISTS chunks_embedding_idx
    ON chunks USING hnsw (embedding vector_cosine_ops)
    WITH (m = 16, ef_construction = 200);

-- Index for looking up chunks by study
CREATE INDEX IF NOT EXISTS chunks_study_id_idx
    ON chunks (study_id);

-- Indexes for study status + domain filtering
CREATE INDEX IF NOT EXISTS studies_status_idx     ON studies (status);
CREATE INDEX IF NOT EXISTS studies_study_type_idx ON studies (study_type);
CREATE INDEX IF NOT EXISTS studies_topic_idx      ON studies (topic);
CREATE INDEX IF NOT EXISTS studies_year_idx       ON studies (year);

-- Full-text search index for keyword baseline comparison
ALTER TABLE chunks ADD COLUMN IF NOT EXISTS tsv tsvector
    GENERATED ALWAYS AS (to_tsvector('english', content)) STORED;

CREATE INDEX IF NOT EXISTS chunks_tsv_idx ON chunks USING gin(tsv);
