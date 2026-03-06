-- +goose Up
ALTER TABLE news_queue RENAME TO search_queue;
ALTER INDEX idx_news_queue_person_id RENAME TO idx_search_queue_person_id;
ALTER INDEX idx_news_queue_added_at RENAME TO idx_search_queue_added_at;
