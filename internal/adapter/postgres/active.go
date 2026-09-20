package postgres

import (
	"context"
	"encoding/json"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/scoring"
	"cs2predictor/internal/platform/common"
)

// The unsettled half of somebody's record, and their medals per chat.

// ActivePredictions lists votes on matches still to be played, soonest
// first. A match with no published time sorts last: it is real, it is just
// not scheduled, and putting it first would push the matches somebody can
// actually plan around off the screen.
func (r *ScoringRepository) ActivePredictions(ctx context.Context, userID common.UserID, limit int) ([]scoring.ActivePrediction, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := executor(ctx, r.pool).Query(ctx, `
		SELECT c.id, c.title, COALESCE(c.stream_language, ''), c.locale,
		       g.code, e.name,
		       m.scheduled_at, p.closes_at,
		       COALESCE(t1.name, ''), COALESCE(t2.name, ''),
		       po.first_score, po.second_score,
		       m.streams
		  FROM prediction_vote v
		  JOIN match_poll p ON p.id = v.poll_id
		  JOIN telegram_chat c ON c.id = p.chat_id
		  JOIN poll_option po ON po.poll_id = v.poll_id AND po.option_index = v.option_index
		  JOIN esport_match m ON m.id = p.match_id
		  JOIN tournament_event e ON e.id = m.event_id
		  JOIN game g ON g.id = e.game_id
		  LEFT JOIN match_team mt1 ON mt1.match_id = m.id AND mt1.position = 1
		  LEFT JOIN match_team mt2 ON mt2.match_id = m.id AND mt2.position = 2
		  LEFT JOIN team t1 ON t1.id = mt1.team_id
		  LEFT JOIN team t2 ON t2.id = mt2.team_id
		 WHERE v.user_id = $1
		   AND m.status IN ('NOT_STARTED', 'RUNNING')
		   -- A prediction on a tournament the chat has since dropped, or in
		   -- a game it has switched off, is not something anybody is still
		   -- waiting on: the chat stopped following it, and the poll will
		   -- never be talked about again in the room it belongs to.
		   AND EXISTS (SELECT 1 FROM event_subscription es
		                WHERE es.chat_id = p.chat_id AND es.event_id = m.event_id AND es.active)
		   AND EXISTS (SELECT 1 FROM chat_enabled_game ceg
		                WHERE ceg.chat_id = p.chat_id AND ceg.game_id = e.game_id)
		 ORDER BY m.scheduled_at ASC NULLS LAST
		 LIMIT $2`, userID.Value, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []scoring.ActivePrediction
	for rows.Next() {
		var row scoring.ActivePrediction
		var streamLanguage, locale string
		var predictedFirst, predictedSecond int
		var streamsRaw []byte
		if err := rows.Scan(&row.ChatID.Value, &row.ChatTitle, &streamLanguage, &locale,
			&row.Game, &row.EventName, &row.ScheduledAt, &row.ClosesAt,
			&row.FirstTeamName, &row.SecondTeamName,
			&predictedFirst, &predictedSecond, &streamsRaw); err != nil {
			return nil, err
		}
		row.PredictedScore = competition.MatchScore{First: predictedFirst, Second: predictedSecond}
		row.StreamURL = streamFor(streamsRaw, streamLanguage, locale)
		out = append(out, row)
	}
	return out, rows.Err()
}

// streamFor picks the broadcast this chat would be shown, reusing the
// domain's own rule rather than a second one written here: the same
// language preference the poll's stream line follows, and never an
// unofficial channel.
func streamFor(raw []byte, streamLanguage, locale string) string {
	if len(raw) == 0 {
		return ""
	}
	var streams []competition.Stream
	if err := json.Unmarshal(raw, &streams); err != nil {
		return ""
	}
	settings := chat.Settings{
		Locale:         common.LocaleFrom(locale),
		StreamLanguage: common.LocaleFrom(streamLanguage),
	}
	if stream, ok := (competition.Match{Streams: streams}).StreamFor(settings.StreamLocale()); ok {
		return stream.URL
	}
	return ""
}

// UserMedals lists the placings the bot has already awarded, newest
// first. Read rather than recomputed: a medal is a decision that was made
// at the time, and rebuilding it from today's standings would quietly
// rewrite history whenever the scoring changed.
func (r *ScoringRepository) UserMedals(ctx context.Context, userID common.UserID) ([]scoring.EventMedal, error) {
	rows, err := executor(ctx, r.pool).Query(ctx, `
		SELECT c.id, c.title, e.name, g.code, em.place, em.awarded_at
		  FROM event_medal em
		  JOIN telegram_chat c ON c.id = em.chat_id
		  JOIN tournament_event e ON e.id = em.event_id
		  JOIN game g ON g.id = e.game_id
		 WHERE em.user_id = $1
		 ORDER BY em.awarded_at DESC, em.place`, userID.Value)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []scoring.EventMedal
	for rows.Next() {
		var medal scoring.EventMedal
		if err := rows.Scan(&medal.ChatID.Value, &medal.ChatTitle, &medal.EventName,
			&medal.Game, &medal.Place, &medal.AwardedAt); err != nil {
			return nil, err
		}
		out = append(out, medal)
	}
	return out, rows.Err()
}
