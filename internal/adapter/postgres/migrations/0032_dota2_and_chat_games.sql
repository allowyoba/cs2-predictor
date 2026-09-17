-- +goose Up
INSERT INTO game(code, display_name) VALUES ('DOTA2', 'Dota 2');

-- Which games a chat wants events/polls for. Row present = enabled; a chat
-- with no rows here has every game disabled — the deliberate default for
-- an existing chat migrating into a multi-game world, not just a new one.
CREATE TABLE chat_enabled_game (
    chat_id     bigint NOT NULL REFERENCES telegram_chat(id) ON DELETE CASCADE,
    game_id     smallint NOT NULL REFERENCES game(id),
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (chat_id, game_id)
);

-- Every chat that already exists was already getting CS2 content — "all
-- games off by default" is the story for a brand-new chat and for Dota2,
-- not a silent regression for everyone already using the bot.
INSERT INTO chat_enabled_game(chat_id, game_id)
SELECT c.id, g.id FROM telegram_chat c, game g WHERE g.code = 'CS2';

-- +goose Down
DROP TABLE IF EXISTS chat_enabled_game;
DELETE FROM game WHERE code = 'DOTA2';
