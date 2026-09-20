-- +goose Up
-- Counter-Strike rankings were matched against the whole team table, so a
-- Dota 2 roster sharing its organisation's name with a CS2 one — BetBoom,
-- Spirit, Falcons and others field both — inherited a ranking it never
-- earned, and polls showed it.
--
-- The matching is now scoped to the games a feed actually ranks (see
-- enrichment.RanksGame). These two deletes remove what the unscoped
-- version wrote. Nothing is lost that cannot be rebuilt: both tables are
-- caches, refilled by the next ranking sync for the teams that legitimately
-- belong to them.
DELETE FROM team_ranking r
 USING team t, game g
 WHERE r.team_id = t.id AND t.game_id = g.id
   AND r.source IN ('VALVE_VRS', 'HLTV')
   AND g.code <> 'CS2';

DELETE FROM team_external_identity i
 USING team t, game g
 WHERE i.team_id = t.id AND t.game_id = g.id
   AND i.provider IN ('VALVE_VRS', 'HLTV')
   AND g.code <> 'CS2';

-- Pending review requests could likewise propose a Dota 2 team as the
-- answer to "which team is this Counter-Strike ranking about". Those
-- candidates are removed, and a request left with nothing to choose from
-- is removed with them — an operator staring at an empty request has no
-- decision available to make.
DELETE FROM team_match_candidate c
 USING team t, game g, team_match_request r
 WHERE c.team_id = t.id AND t.game_id = g.id AND c.request_id = r.id
   AND r.source IN ('VALVE_VRS', 'HLTV')
   AND g.code <> 'CS2';

DELETE FROM team_match_request r
 WHERE r.source IN ('VALVE_VRS', 'HLTV')
   AND r.status = 'pending'
   AND NOT EXISTS (SELECT 1 FROM team_match_candidate c WHERE c.request_id = r.id);

-- +goose Down
-- Nothing to restore: the rows deleted above were wrong, and the tables
-- they lived in are caches the sync refills on its own.
SELECT 1;
