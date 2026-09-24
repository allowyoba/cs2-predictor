package postgres

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"cs2predictor/internal/domain/scoring"
	"cs2predictor/internal/platform/common"
)

// periodClause and userPeriodClause are otherwise only exercised through
// the integration suite (needs Postgres); teamClause's own placeholder
// numbering is worth a pure unit test since it is easy to get wrong when a
// clause is appended after others that already consumed args.
func TestTeamClause_NilOmitsClause(t *testing.T) {
	if got := teamClause(nil, func(any) string { return "$X" }); got != "" {
		t.Fatalf("expected empty clause for nil team, got %q", got)
	}
}

func TestPeriodClause_EventPlusTeam_ArgsOrderedAndNumbered(t *testing.T) {
	team := common.TeamID{Value: uuid.New()}
	period := scoring.ForEvent(common.EventID{Value: uuid.New()}).ForTeam(&team)

	clause, args := periodClause(period, nil)
	if len(args) != 2 {
		t.Fatalf("expected 2 args (event id, team id), got %d: %v", len(args), args)
	}
	if !strings.Contains(clause, "m.event_id = $2") {
		t.Fatalf("expected event clause at $2, got %q", clause)
	}
	if !strings.Contains(clause, "match_team mt") || !strings.Contains(clause, "mt.team_id = $3") {
		t.Fatalf("expected team clause at $3, got %q", clause)
	}
}

func TestUserPeriodClause_EventPlusTeam(t *testing.T) {
	team := common.TeamID{Value: uuid.New()}
	period := scoring.ForEvent(common.EventID{Value: uuid.New()}).ForTeam(&team)

	clause, args := userPeriodClause(period)
	if len(args) != 2 {
		t.Fatalf("expected 2 args, got %d: %v", len(args), args)
	}
	if !strings.Contains(clause, "mt.team_id") {
		t.Fatalf("expected team scoping in user clause, got %q", clause)
	}
}
