package pandascore

import "time"

type namedDTO struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Location string `json:"location"`
}

type seriesDTO struct {
	ID       int64      `json:"id"`
	Name     string     `json:"name"`
	FullName string     `json:"full_name"`
	BeginAt  *time.Time `json:"begin_at"`
	EndAt    *time.Time `json:"end_at"`
	Year     *int       `json:"year"`
	Status   *string    `json:"status"`
	Tier     *string    `json:"tier"` // legacy/backward-compatible fallback; current tiering is tournament-level
	League   namedDTO   `json:"league"`
}

type tournamentDTO struct {
	ID       int64      `json:"id"`
	Name     string     `json:"name"`
	BeginAt  *time.Time `json:"begin_at"`
	EndAt    *time.Time `json:"end_at"`
	Tier     *string    `json:"tier"`
	SerieID  int64      `json:"serie_id"`
	Serie    seriesDTO  `json:"serie"`
	LeagueID int64      `json:"league_id"`
	League   namedDTO   `json:"league"`
}

type opponentDTO struct {
	Opponent *namedDTO `json:"opponent"`
}

type resultDTO struct {
	TeamID int64 `json:"team_id"`
	Score  int   `json:"score"`
}

type matchDTO struct {
	ID            int64         `json:"id"`
	Name          *string       `json:"name"`
	Status        string        `json:"status"`
	BeginAt       *time.Time    `json:"begin_at"`
	EndAt         *time.Time    `json:"end_at"`
	MatchType     *string       `json:"match_type"`
	NumberOfGames *int          `json:"number_of_games"`
	Serie         namedDTO      `json:"serie"`
	Tournament    *namedDTO     `json:"tournament"`
	Opponents     []opponentDTO `json:"opponents"`
	Results       []resultDTO   `json:"results"`
	Forfeit       bool          `json:"forfeit"`
}
