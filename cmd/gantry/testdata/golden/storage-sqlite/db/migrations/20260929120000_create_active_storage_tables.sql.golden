-- Attached files (gantry g storage), in Active Storage's shape: a blob is a
-- stored file, and an attachment names it on a record, so a Rails app's
-- rows and files carry over.
-- +goose Up
CREATE TABLE "active_storage_blobs" (
  "id" integer PRIMARY KEY AUTOINCREMENT NOT NULL,
  "key" varchar NOT NULL,
  "filename" varchar NOT NULL,
  "content_type" varchar,
  "metadata" text,
  "service_name" varchar NOT NULL,
  "byte_size" bigint NOT NULL,
  "checksum" varchar,
  "created_at" datetime NOT NULL
);
CREATE UNIQUE INDEX "index_active_storage_blobs_on_key" ON "active_storage_blobs" ("key");
CREATE TABLE "active_storage_attachments" (
  "id" integer PRIMARY KEY AUTOINCREMENT NOT NULL,
  "name" varchar NOT NULL,
  "record_type" varchar NOT NULL,
  "record_id" bigint NOT NULL,
  "blob_id" bigint NOT NULL REFERENCES "active_storage_blobs" ("id"),
  "created_at" datetime NOT NULL
);
CREATE INDEX "index_active_storage_attachments_on_blob_id" ON "active_storage_attachments" ("blob_id");
CREATE UNIQUE INDEX "index_active_storage_attachments_uniqueness" ON "active_storage_attachments" ("record_type", "record_id", "name", "blob_id");

-- +goose Down
DROP TABLE "active_storage_attachments";
DROP TABLE "active_storage_blobs";
