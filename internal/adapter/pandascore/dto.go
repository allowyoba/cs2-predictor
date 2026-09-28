package pandascore

import "time"

type namedDTO struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Location string `json:"location"`
	// ImageURL is the team's crest on PandaScore's own CDN. It is the only
	// logo source that covers every game this bot follows — HLTV's ranking
	// covers Counter-Strike alone — so it is the baseline, and a better
	// per-game source overrides it where one exists.
	ImageURL string `json:"image_url"`
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

type streamDTO struct {
	Language string `json:"language"`
	RawURL   string `json:"raw_url"`
	Main     bool   `json:"main"`
	Official bool   `json:"official"`
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
	StreamsList   []streamDTO   `json:"streams_list"`
}

// playerDTO is PandaScore's player object as its own docs sample it (see
// the /players response example): "name" is the nickname the scene uses, and
// first/last name are the real one, often null.
type playerDTO struct {
	ID          int64   `json:"id"`
	Name        string  `json:"name"`
	FirstName   *string `json:"first_name"`
	LastName    *string `json:"last_name"`
	Nationality *string `json:"nationality"`
	ImageURL    *string `json:"image_url"`
	Role        *string `json:"role"`
}

// teamRosterDTO is the team object trimmed to what a roster sync needs. The
// team endpoints return "players" inline, so a roster costs no separate
// request per player.
type teamRosterDTO struct {
	ID      int64       `json:"id"`
	Name    string      `json:"name"`
	Players []playerDTO `json:"players"`
}
