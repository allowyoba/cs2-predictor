-- +goose Up
-- valve_vrs_snapshot caches every team Valve's own regional-standings feed
-- currently reports, regardless of whether it has been matched to a local
-- team yet — the raw material the fuzzy-match/crowd-review pipeline
-- searches against when a newly-seen team can't be resolved by exact name/
-- alias/roster. Kept separate from team_ranking (which only ever holds
-- rows for teams that DID match) so "not currently in Valve's list at all"
-- stays distinguishable from "in the list, but under a different name".
CREATE TABLE valve_vrs_snapshot (
    normalized_name     varchar(200) PRIMARY KEY,
    external_name       varchar(200) NOT NULL,
    global_rank         integer,
    points              integer,
    updated_at          timestamptz NOT NULL DEFAULT now()
);

-- team_match_request is one externally-reported team name (so far only
-- Valve VRS) that couldn't be resolved automatically with high confidence,
-- awaiting either an operator's decision or enough crowd input to raise its
-- best candidate above the auto-accept bar on a later sync.
CREATE TABLE team_match_request (
    id                  uuid PRIMARY KEY,
    external_name       varchar(200) NOT NULL,
    source              varchar(32) NOT NULL,
    status              varchar(16) NOT NULL DEFAULT 'pending', -- pending | confirmed | rejected
    best_team_id        uuid REFERENCES team(id),
    best_score          integer NOT NULL DEFAULT 0,
    crowd_asks_sent     integer NOT NULL DEFAULT 0,
    created_at          timestamptz NOT NULL DEFAULT now(),
    resolved_at         timestamptz,
    UNIQUE (source, external_name)
);
CREATE INDEX idx_team_match_request_status ON team_match_request(status);

-- team_match_candidate is one local team considered for a request, with the
-- score that ranks it (0-100). score_kind records what produced it
-- (fuzzy name similarity, or crowd votes nudging it up/down) purely for
-- operator-facing transparency.
CREATE TABLE team_match_candidate (
    request_id          uuid NOT NULL REFERENCES team_match_request(id) ON DELETE CASCADE,
    team_id             uuid NOT NULL REFERENCES team(id) ON DELETE CASCADE,
    score               integer NOT NULL,
    score_kind          varchar(16) NOT NULL, -- fuzzy | crowd
    PRIMARY KEY (request_id, team_id)
);

-- team_match_response is one helper's yes/no answer to "is <external_name>
-- the same team as <candidate>?" — kept individually (not just folded into
-- the candidate's score) so a request's card can show "3 yes / 1 no" and so
-- the same person is never asked about the same request twice.
CREATE TABLE team_match_response (
    id                  uuid PRIMARY KEY,
    request_id          uuid NOT NULL REFERENCES team_match_request(id) ON DELETE CASCADE,
    user_id             bigint NOT NULL REFERENCES telegram_user(id),
    candidate_team_id   uuid NOT NULL REFERENCES team(id),
    answer              varchar(8) NOT NULL, -- yes | no
    responded_at        timestamptz NOT NULL DEFAULT now(),
    UNIQUE (request_id, user_id)
);

-- team_match_helper_pref tracks, per person, whether they've opted out of
-- ever being asked one of these questions again, and how many they've
-- answered in total — the lifetime quota that keeps this from becoming a
-- recurring chore for anyone who hasn't explicitly agreed to keep helping.
CREATE TABLE team_match_helper_pref (
    user_id             bigint PRIMARY KEY REFERENCES telegram_user(id) ON DELETE CASCADE,
    opted_out           boolean NOT NULL DEFAULT false,
    asked_count         integer NOT NULL DEFAULT 0,
    last_asked_at       timestamptz
);

-- +goose Down
DROP TABLE IF EXISTS team_match_helper_pref;
DROP TABLE IF EXISTS team_match_response;
DROP TABLE IF EXISTS team_match_candidate;
DROP TABLE IF EXISTS team_match_request;
DROP TABLE IF EXISTS valve_vrs_snapshot;
