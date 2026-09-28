-- Records the votes cast in the chat after the PGL Wallachia Season 9 final's
-- poll had already closed, and the points they earn.
--
-- The match started sooner than expected and nobody got to the poll in time,
-- so the predictions were made in the room instead. They are real
-- predictions, made before the result was known, and the tournament table is
-- wrong without them.
--
-- Run it against the bot's database:
--
--   docker exec -i <postgres-container> psql -U <user> -d <db> \
--     -v ON_ERROR_STOP=1 -f - < wallachia-final-late-votes.sql
--
-- It is one transaction and it is safe to run twice: every insert is an
-- upsert, so a second run changes nothing. It refuses to do anything unless
-- the chat, the tournament, the final and its poll each resolve to exactly
-- one row — the ids are not hard-coded, so a wrong assumption stops the
-- script rather than writing into the wrong tournament.
--
-- What it writes:
--   @serjinho            3:0 for Team Yandex — exactly right, 3 points
--   every other voter    3:1 for Team Yandex — right winner,   1 point
--
-- "Every other voter" is whoever has voted on any other poll of this same
-- tournament in this chat: the people who were playing along, which is what
-- "the rest of the chat" means here. They are listed in the notice the
-- script prints before it commits.

BEGIN;

DO $$
DECLARE
    v_chat_id      bigint;
    v_event_id     uuid;
    v_match_id     uuid;
    v_poll_id      uuid;
    v_yandex_first boolean;
    v_exact_option integer;
    v_close_option integer;
    v_serjinho     bigint;
    v_voted_at     timestamptz;
    v_others       bigint[];
BEGIN
    SELECT id INTO STRICT v_chat_id
      FROM telegram_chat
     WHERE title ILIKE '%клоун%';

    SELECT e.id INTO STRICT v_event_id
      FROM tournament_event e
      JOIN game g ON g.id = e.game_id
     WHERE g.code = 'DOTA2'
       AND e.name ILIKE '%wallachia%'
       AND e.name ILIKE '%9%';

    -- The final is the last match the tournament played.
    SELECT m.id INTO STRICT v_match_id
      FROM esport_match m
     WHERE m.event_id = v_event_id
       AND m.status = 'FINISHED'
       AND COALESCE(m.actual_started_at, m.scheduled_at) = (
             SELECT MAX(COALESCE(actual_started_at, scheduled_at))
               FROM esport_match WHERE event_id = v_event_id AND status = 'FINISHED');

    SELECT p.id INTO STRICT v_poll_id
      FROM match_poll p
     WHERE p.chat_id = v_chat_id AND p.match_id = v_match_id;

    -- Which side of the scoreline Team Yandex is on. Without this, "3:0 for
    -- Yandex" could be written as a 3:0 win for their opponent.
    SELECT (t.name ILIKE '%yandex%') INTO STRICT v_yandex_first
      FROM match_team mt
      JOIN team t ON t.id = mt.team_id
     WHERE mt.match_id = v_match_id AND mt.position = 1;

    SELECT option_index INTO STRICT v_exact_option
      FROM poll_option
     WHERE poll_id = v_poll_id
       AND first_score  = CASE WHEN v_yandex_first THEN 3 ELSE 0 END
       AND second_score = CASE WHEN v_yandex_first THEN 0 ELSE 3 END;

    SELECT option_index INTO STRICT v_close_option
      FROM poll_option
     WHERE poll_id = v_poll_id
       AND first_score  = CASE WHEN v_yandex_first THEN 3 ELSE 1 END
       AND second_score = CASE WHEN v_yandex_first THEN 1 ELSE 3 END;

    -- By username, falling back to the display name: Telegram only sends a
    -- username when the person has one, so the column can be empty for
    -- somebody who has been voting for months. STRICT either way — two
    -- people matching is a reason to stop, not to pick one.
    SELECT id INTO STRICT v_serjinho
      FROM telegram_user
     WHERE username ILIKE 'serjinho' OR (username IS NULL AND display_name ILIKE '%serjinho%');

    -- Dated to just before the poll closed: these were predictions, and a
    -- vote stamped after the match would read as one made knowing the result.
    SELECT closes_at - interval '1 minute' INTO v_voted_at
      FROM match_poll WHERE id = v_poll_id;

    -- Everyone who was playing along in this tournament, minus whoever
    -- already has a vote on this very poll.
    SELECT COALESCE(array_agg(DISTINCT v.user_id), '{}') INTO v_others
      FROM prediction_vote v
      JOIN match_poll p ON p.id = v.poll_id
      JOIN esport_match m ON m.id = p.match_id
     WHERE p.chat_id = v_chat_id
       AND m.event_id = v_event_id
       AND v.user_id <> v_serjinho
       AND NOT EXISTS (SELECT 1 FROM prediction_vote e
                        WHERE e.poll_id = v_poll_id AND e.user_id = v.user_id);

    RAISE NOTICE 'chat=% event=% match=% poll=%', v_chat_id, v_event_id, v_match_id, v_poll_id;
    RAISE NOTICE 'Team Yandex is the % team; 3:0 is option %, 3:1 is option %',
        CASE WHEN v_yandex_first THEN 'first' ELSE 'second' END, v_exact_option, v_close_option;
    RAISE NOTICE 'serjinho=% others=%', v_serjinho, v_others;

    -- @serjinho: 3:0, exactly right.
    INSERT INTO prediction_vote(poll_id, user_id, option_index, voted_at)
    VALUES (v_poll_id, v_serjinho, v_exact_option, v_voted_at)
    ON CONFLICT (poll_id, user_id) DO UPDATE SET option_index = excluded.option_index;

    INSERT INTO score_award(poll_id, user_id, points, kind, awarded_at)
    VALUES (v_poll_id, v_serjinho, 3, 'EXACT_SCORE', now())
    ON CONFLICT (poll_id, user_id) DO UPDATE SET points = excluded.points, kind = excluded.kind;

    -- Everyone else: 3:1, the right winner.
    INSERT INTO prediction_vote(poll_id, user_id, option_index, voted_at)
    SELECT v_poll_id, u, v_close_option, v_voted_at FROM unnest(v_others) AS u
    ON CONFLICT (poll_id, user_id) DO UPDATE SET option_index = excluded.option_index;

    INSERT INTO score_award(poll_id, user_id, points, kind, awarded_at)
    SELECT v_poll_id, u, 1, 'OUTCOME', now() FROM unnest(v_others) AS u
    ON CONFLICT (poll_id, user_id) DO UPDATE SET points = excluded.points, kind = excluded.kind;
END $$;

-- The tournament table as it now stands, so the result can be read before
-- the transaction is committed.
SELECT u.display_name,
       SUM(a.points)                                           AS points,
       COUNT(*) FILTER (WHERE a.kind = 'EXACT_SCORE')          AS exact_hits,
       COUNT(*)                                                AS scored
  FROM score_award a
  JOIN match_poll p   ON p.id = a.poll_id
  JOIN esport_match m ON m.id = p.match_id
  JOIN telegram_user u ON u.id = a.user_id
 WHERE m.event_id = (SELECT e.id FROM tournament_event e JOIN game g ON g.id = e.game_id
                      WHERE g.code = 'DOTA2' AND e.name ILIKE '%wallachia%' AND e.name ILIKE '%9%')
   AND p.chat_id = (SELECT id FROM telegram_chat WHERE title ILIKE '%клоун%')
 GROUP BY u.display_name
 ORDER BY points DESC, exact_hits DESC;

COMMIT;
