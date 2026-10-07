-- Таблица связей между сообщениями в разных мессенджерах.
-- Одна запись = "сообщение X в чате A было переслано как сообщение Y в чат B".
CREATE TABLE message_links (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    source_platform   TEXT NOT NULL,
    source_chat       TEXT NOT NULL,
    source_message_id TEXT NOT NULL,
    target_platform   TEXT NOT NULL,
    target_chat       TEXT NOT NULL,
    target_message_id TEXT NOT NULL,
    created_at        INTEGER NOT NULL DEFAULT (unixepoch()),

    UNIQUE(source_platform, source_chat, source_message_id,
           target_platform, target_chat)
);

-- Индекс для обратного поиска: "кто является источником этого target-сообщения?"
CREATE INDEX idx_message_links_target
    ON message_links(target_platform, target_chat, target_message_id);