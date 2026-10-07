-- Хранилище сессий Telegram (MTProto).
-- Одна строка = один инстанс адаптера с его сессией.
-- Данные сессии — это сериализованное состояние ключей шифрования.
CREATE TABLE telegram_sessions (
    name       TEXT PRIMARY KEY,
    data       BLOB NOT NULL,
    updated_at INTEGER NOT NULL DEFAULT (unixepoch())
);