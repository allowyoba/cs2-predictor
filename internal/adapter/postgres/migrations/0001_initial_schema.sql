-- +goose Up
CREATE TABLE data_provider (
    id              smallserial PRIMARY KEY,
    code            varchar(32) NOT NULL UNIQUE,
    display_name    varchar(100) NOT NULL
);

CREATE TABLE game (
    id              smallserial PRIMARY KEY,
    code            varchar(32) NOT NULL UNIQUE,
    display_name    varchar(100) NOT NULL
);

CREATE TABLE tournament_event (
    id              uuid PRIMARY KEY,
    game_id         smallint NOT NULL REFERENCES game(id),
    provider_id     smallint NOT NULL REFERENCES data_provider(id),
    external_id     varchar(100) NOT NULL,
    name            varchar(300) NOT NULL,
    status          varchar(32) NOT NULL,
    starts_at       timestamptz,
    ends_at         timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (provider_id, external_id)
);

CREATE TABLE event_stage (
    id              uuid PRIMARY KEY,
    event_id        uuid NOT NULL REFERENCES tournament_event(id),
    provider_id     smallint NOT NULL REFERENCES data_provider(id),
    external_id     varchar(100) NOT NULL,
    name            varchar(300) NOT NULL,
    UNIQUE (provider_id, external_id)
);

CREATE TABLE team (
    id              uuid PRIMARY KEY,
    game_id         smallint NOT NULL REFERENCES game(id),
    provider_id     smallint NOT NULL REFERENCES data_provider(id),
    external_id     varchar(100) NOT NULL,
    name            varchar(200) NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (provider_id, external_id)
);

CREATE TABLE esport_match (
    id                  uuid PRIMARY KEY,
    event_id            uuid NOT NULL REFERENCES tournament_event(id),
    stage_id            uuid REFERENCES event_stage(id),
    provider_id         smallint NOT NULL REFERENCES data_provider(id),
    external_id         varchar(100) NOT NULL,
    status              varchar(32) NOT NULL,
    series_kind         varchar(32) NOT NULL,
    series_size         integer NOT NULL CHECK (series_size > 0),
    scheduled_at        timestamptz,
    actual_started_at   timestamptz,
    finished_at         timestamptz,
    first_score         integer CHECK (first_score >= 0),
    second_score        integer CHECK (second_score >= 0),
    version             bigint NOT NULL DEFAULT 0,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    UNIQUE (provider_id, external_id),
    CHECK ((first_score IS NULL) = (second_score IS NULL))
);

CREATE TABLE match_team (
    match_id        uuid NOT NULL REFERENCES esport_match(id) ON DELETE CASCADE,
    position        smallint NOT NULL CHECK (position IN (1, 2)),
    team_id         uuid NOT NULL REFERENCES team(id),
    PRIMARY KEY (match_id, position),
    UNIQUE (match_id, team_id)
);

CREATE TABLE telegram_chat (
    id                  bigint PRIMARY KEY,
    title               varchar(300) NOT NULL,
    locale              varchar(10) NOT NULL DEFAULT 'RU',
    timezone            varchar(100) NOT NULL DEFAULT 'Europe/Moscow',
    default_topic_id    bigint,
    active              boolean NOT NULL DEFAULT true,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE telegram_user (
    id              bigint PRIMARY KEY,
    username        varchar(100),
    display_name    varchar(300) NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE chat_moderator (
    chat_id         bigint NOT NULL REFERENCES telegram_chat(id) ON DELETE CASCADE,
    user_id         bigint NOT NULL REFERENCES telegram_user(id),
    appointed_by    bigint NOT NULL REFERENCES telegram_user(id),
    appointed_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (chat_id, user_id)
);

CREATE TABLE event_subscription (
    chat_id         bigint NOT NULL REFERENCES telegram_chat(id) ON DELETE CASCADE,
    event_id        uuid NOT NULL REFERENCES tournament_event(id) ON DELETE CASCADE,
    subscribed_at   timestamptz NOT NULL,
    active          boolean NOT NULL DEFAULT true,
    PRIMARY KEY (chat_id, event_id)
);

CREATE TABLE event_topic (
    chat_id         bigint NOT NULL REFERENCES telegram_chat(id) ON DELETE CASCADE,
    event_id        uuid NOT NULL REFERENCES tournament_event(id) ON DELETE CASCADE,
    topic_id        bigint NOT NULL,
    PRIMARY KEY (chat_id, event_id)
);

CREATE TABLE match_poll (
    id                      uuid PRIMARY KEY,
    chat_id                 bigint NOT NULL REFERENCES telegram_chat(id) ON DELETE CASCADE,
    match_id                uuid NOT NULL REFERENCES esport_match(id) ON DELETE CASCADE,
    topic_id                bigint,
    telegram_poll_id        varchar(100) UNIQUE,
    telegram_message_id     bigint,
    status                  varchar(32) NOT NULL,
    closes_at               timestamptz NOT NULL,
    created_at              timestamptz NOT NULL DEFAULT now(),
    updated_at              timestamptz NOT NULL DEFAULT now(),
    UNIQUE (chat_id, match_id)
);

CREATE TABLE poll_option (
    poll_id         uuid NOT NULL REFERENCES match_poll(id) ON DELETE CASCADE,
    option_index    integer NOT NULL CHECK (option_index >= 0),
    first_score     integer NOT NULL CHECK (first_score >= 0),
    second_score    integer NOT NULL CHECK (second_score >= 0),
    PRIMARY KEY (poll_id, option_index),
    UNIQUE (poll_id, first_score, second_score)
);

CREATE TABLE prediction_vote (
    poll_id         uuid NOT NULL REFERENCES match_poll(id) ON DELETE CASCADE,
    user_id         bigint NOT NULL REFERENCES telegram_user(id),
    option_index    integer NOT NULL,
    voted_at        timestamptz NOT NULL,
    PRIMARY KEY (poll_id, user_id),
    FOREIGN KEY (poll_id, option_index) REFERENCES poll_option(poll_id, option_index)
);

CREATE TABLE score_award (
    id                  uuid PRIMARY KEY,
    chat_id             bigint NOT NULL REFERENCES telegram_chat(id) ON DELETE CASCADE,
    event_id            uuid NOT NULL REFERENCES tournament_event(id),
    match_id            uuid NOT NULL REFERENCES esport_match(id),
    poll_id             uuid NOT NULL REFERENCES match_poll(id) ON DELETE CASCADE,
    user_id             bigint NOT NULL REFERENCES telegram_user(id),
    points              integer NOT NULL CHECK (points > 0),
    kind                varchar(32) NOT NULL,
    match_started_at    timestamptz NOT NULL,
    awarded_at          timestamptz NOT NULL,
    UNIQUE (poll_id, user_id)
);

CREATE TABLE event_medal (
    chat_id         bigint NOT NULL REFERENCES telegram_chat(id) ON DELETE CASCADE,
    event_id        uuid NOT NULL REFERENCES tournament_event(id),
    user_id         bigint NOT NULL REFERENCES telegram_user(id),
    place           smallint NOT NULL CHECK (place BETWEEN 1 AND 3),
    awarded_at      timestamptz NOT NULL,
    PRIMARY KEY (chat_id, event_id, user_id)
);

CREATE TABLE processed_telegram_update (
    update_id       bigint PRIMARY KEY,
    processed_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE outbox_event (
    id              uuid PRIMARY KEY,
    aggregate_type  varchar(100) NOT NULL,
    aggregate_id    varchar(100) NOT NULL,
    event_type      varchar(200) NOT NULL,
    payload         jsonb NOT NULL,
    occurred_at     timestamptz NOT NULL,
    published_at    timestamptz,
    attempts        integer NOT NULL DEFAULT 0,
    last_error      text
);

CREATE INDEX idx_event_status ON tournament_event(status);
CREATE INDEX idx_match_event_status ON esport_match(event_id, status);
CREATE INDEX idx_match_schedule ON esport_match(status, scheduled_at);
CREATE INDEX idx_poll_due ON match_poll(status, closes_at);
CREATE INDEX idx_award_chat_started ON score_award(chat_id, match_started_at);
CREATE INDEX idx_award_chat_event ON score_award(chat_id, event_id);
CREATE INDEX idx_outbox_pending ON outbox_event(occurred_at) WHERE published_at IS NULL;

INSERT INTO data_provider(code, display_name) VALUES ('PANDASCORE', 'PandaScore');
INSERT INTO game(code, display_name) VALUES ('CS2', 'Counter-Strike 2');

-- +goose Down
-- (forward-only: no rollback provided)
