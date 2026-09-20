-- +goose Up
-- Three places where the schema stored something that was not a single
-- value in a single column, and one where it stored the same fact twice.
--
-- 1. A match's broadcasts were a JSON array. A broadcast is an independent
--    fact — a language, a URL, whether it is the main one, whether it is
--    official — and the code already unpacked the array on every read to
--    pick one row out of it. Picking a row out of a set is what a table is
--    for.
CREATE TABLE match_stream (
    match_id  uuid    NOT NULL REFERENCES esport_match(id) ON DELETE CASCADE,
    url       text    NOT NULL,
    language  text    NOT NULL DEFAULT '',
    main      boolean NOT NULL DEFAULT false,
    official  boolean NOT NULL DEFAULT false,
    PRIMARY KEY (match_id, url)
);

-- The one query shape there is: this match's broadcasts, best first.
CREATE INDEX match_stream_pick_idx ON match_stream (match_id, official DESC, main DESC);

INSERT INTO match_stream (match_id, url, language, main, official)
SELECT m.id,
       s->>'url',
       COALESCE(s->>'language', ''),
       COALESCE((s->>'main')::boolean, false),
       COALESCE((s->>'official')::boolean, false)
  FROM esport_match m
  CROSS JOIN LATERAL jsonb_array_elements(m.streams) AS s
 WHERE jsonb_typeof(m.streams) = 'array'
   AND COALESCE(s->>'url', '') <> ''
ON CONFLICT (match_id, url) DO NOTHING;

ALTER TABLE esport_match DROP COLUMN streams;

-- 2. A ranking snapshot's roster was a text[]. Same story: a player is a
--    fact about that snapshot, and position in the array is information
--    the array type does not promise to keep.
CREATE TABLE team_ranking_player (
    team_id  uuid NOT NULL,
    source   text NOT NULL,
    position smallint NOT NULL,
    player   text NOT NULL,
    PRIMARY KEY (team_id, source, position),
    FOREIGN KEY (team_id, source) REFERENCES team_ranking(team_id, source) ON DELETE CASCADE
);

INSERT INTO team_ranking_player (team_id, source, position, player)
SELECT r.team_id, r.source, p.ordinality, p.player
  FROM team_ranking r
  CROSS JOIN LATERAL unnest(r.roster) WITH ORDINALITY AS p(player, ordinality)
 WHERE r.roster IS NOT NULL AND COALESCE(p.player, '') <> ''
ON CONFLICT DO NOTHING;

ALTER TABLE team_ranking DROP COLUMN roster;

-- 3. score_award carried chat_id, event_id, match_id and match_started_at
--    alongside poll_id. Every one of them is reachable from the poll —
--    match_poll has the chat and the match, esport_match has the event and
--    when it started — so they are the same fact written twice, free to
--    drift and impossible to notice drifting.
--
--    They are also never read. Every query in this repository joins
--    score_award on (poll_id, user_id) and nothing else, which production
--    confirms: a million scans on that unique index and exactly zero on
--    either of the two indexes built over these columns.
DROP INDEX IF EXISTS idx_award_chat_event;
DROP INDEX IF EXISTS idx_award_chat_started;
ALTER TABLE score_award DROP COLUMN chat_id;
ALTER TABLE score_award DROP COLUMN event_id;
ALTER TABLE score_award DROP COLUMN match_id;
ALTER TABLE score_award DROP COLUMN match_started_at;

-- The surrogate key went unused too — zero scans on its index — while
-- (poll_id, user_id) is the real identity of an award and already had a
-- unique constraint. One key, and it is the one that means something.
ALTER TABLE score_award DROP CONSTRAINT score_award_pkey;
ALTER TABLE score_award DROP COLUMN id;
-- Dropping the old unique constraint first: its index is what the new
-- primary key would otherwise duplicate, and an index behind a constraint
-- cannot be dropped on its own.
ALTER TABLE score_award DROP CONSTRAINT IF EXISTS score_award_poll_id_user_id_key;
ALTER TABLE score_award ADD PRIMARY KEY (poll_id, user_id);

-- +goose Down
ALTER TABLE esport_match ADD COLUMN streams jsonb;
UPDATE esport_match m SET streams = (
    SELECT jsonb_agg(jsonb_build_object('url', s.url, 'language', s.language, 'main', s.main, 'official', s.official))
      FROM match_stream s WHERE s.match_id = m.id);
DROP TABLE IF EXISTS match_stream;

ALTER TABLE team_ranking ADD COLUMN roster text[];
UPDATE team_ranking r SET roster = (
    SELECT array_agg(p.player ORDER BY p.position)
      FROM team_ranking_player p WHERE p.team_id = r.team_id AND p.source = r.source);
DROP TABLE IF EXISTS team_ranking_player;

ALTER TABLE score_award DROP CONSTRAINT score_award_pkey;
ALTER TABLE score_award ADD COLUMN id uuid NOT NULL DEFAULT gen_random_uuid();
ALTER TABLE score_award ADD PRIMARY KEY (id);
CREATE UNIQUE INDEX score_award_poll_id_user_id_key ON score_award (poll_id, user_id);
ALTER TABLE score_award ADD COLUMN chat_id bigint;
ALTER TABLE score_award ADD COLUMN event_id uuid;
ALTER TABLE score_award ADD COLUMN match_id uuid;
ALTER TABLE score_award ADD COLUMN match_started_at timestamptz;
UPDATE score_award a SET chat_id = p.chat_id, match_id = p.match_id,
       event_id = m.event_id, match_started_at = COALESCE(m.actual_started_at, m.scheduled_at)
  FROM match_poll p JOIN esport_match m ON m.id = p.match_id
 WHERE p.id = a.poll_id;
CREATE INDEX idx_award_chat_event ON score_award(chat_id, event_id);
CREATE INDEX idx_award_chat_started ON score_award(chat_id, match_started_at);
