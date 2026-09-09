//go:build integration

package postgres_test

import (
	"testing"
	"time"

	pg "cs2predictor/internal/adapter/postgres"
	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/enrichment"
	"cs2predictor/internal/platform/common"
)

func timeMustParse(t *testing.T, date string) time.Time {
	t.Helper()
	ts, err := time.Parse("2006-01-02", date)
	if err != nil {
		t.Fatal(err)
	}
	return ts
}

func TestEnrichmentRepository_ListTeamsSeesTeamsSavedViaCompetitionRepository(t *testing.T) {
	pool, ctx := newTestPool(t)
	catalog := pg.NewCompetitionRepository(pool)
	enrichmentRepo := pg.NewEnrichmentRepository(pool)

	event := competition.Event{ID: common.NewEventID(), Game: competition.GameCS2, Name: "E", ExternalID: "e1", Status: competition.EventUpcoming, Provider: "PANDASCORE"}
	if _, err := catalog.SaveEvent(ctx, event); err != nil {
		t.Fatal(err)
	}
	format, _ := competition.NewSeriesFormat(competition.BestOf, 3)
	spirit := competition.Team{ID: common.NewTeamID(), Name: "Spirit", ExternalID: "t1", Location: "ru"}
	navi := competition.Team{ID: common.NewTeamID(), Name: "Natus Vincere", ExternalID: "t2", Location: "ua"}
	match := competition.Match{ID: common.NewMatchID(), EventID: event.ID, ExternalID: "m1", Status: competition.MatchNotStarted, Format: format, FirstTeam: &spirit, SecondTeam: &navi}
	if _, err := catalog.SaveMatch(ctx, match); err != nil {
		t.Fatal(err)
	}

	teams, err := enrichmentRepo.ListTeams(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[common.TeamID]competition.Team{}
	for _, tm := range teams {
		byID[tm.ID] = tm
	}
	if got, ok := byID[spirit.ID]; !ok || got.Name != "Spirit" || got.Location != "ru" {
		t.Fatalf("ListTeams missing/wrong Spirit: %+v (ok=%v)", got, ok)
	}
	if got, ok := byID[navi.ID]; !ok || got.Name != "Natus Vincere" {
		t.Fatalf("ListTeams missing/wrong Natus Vincere: %+v (ok=%v)", got, ok)
	}
}

func TestEnrichmentRepository_SaveAndFindRankingRoundTrips(t *testing.T) {
	pool, ctx := newTestPool(t)
	catalog := pg.NewCompetitionRepository(pool)
	repo := pg.NewEnrichmentRepository(pool)

	event := competition.Event{ID: common.NewEventID(), Game: competition.GameCS2, Name: "E", ExternalID: "e1", Status: competition.EventUpcoming, Provider: "PANDASCORE"}
	if _, err := catalog.SaveEvent(ctx, event); err != nil {
		t.Fatal(err)
	}
	format, _ := competition.NewSeriesFormat(competition.BestOf, 3)
	spirit := competition.Team{ID: common.NewTeamID(), Name: "Spirit", ExternalID: "t1"}
	match := competition.Match{ID: common.NewMatchID(), EventID: event.ID, ExternalID: "m1", Status: competition.MatchNotStarted, Format: format, FirstTeam: &spirit}
	if _, err := catalog.SaveMatch(ctx, match); err != nil {
		t.Fatal(err)
	}

	// Missing ranking must come back as (nil, nil), not an error.
	missing, err := repo.FindRanking(ctx, spirit.ID, enrichment.SourceValveVRS)
	if err != nil || missing != nil {
		t.Fatalf("FindRanking before any save = %v, %v, want nil, nil", missing, err)
	}

	globalRank, points := 1, 2011
	published := timeMustParse(t, "2026-08-03")
	ranking := enrichment.TeamRanking{
		TeamID: spirit.ID, GlobalRank: &globalRank, Points: &points,
		Roster: []string{"donk", "magixx", "sh1ro"}, PublishedAt: published, Source: enrichment.SourceValveVRS,
	}
	if err := repo.SaveRanking(ctx, ranking); err != nil {
		t.Fatal(err)
	}

	got, err := repo.FindRanking(ctx, spirit.ID, enrichment.SourceValveVRS)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.GlobalRank == nil || *got.GlobalRank != 1 || got.Points == nil || *got.Points != 2011 {
		t.Fatalf("FindRanking = %+v, want GlobalRank=1 Points=2011", got)
	}
	if len(got.Roster) != 3 || got.Roster[0] != "donk" {
		t.Fatalf("Roster = %v, want [donk magixx sh1ro]", got.Roster)
	}
	if !got.PublishedAt.Equal(published) {
		t.Fatalf("PublishedAt = %v, want %v", got.PublishedAt, published)
	}

	// Saving again for the same (team, source) must upsert, not duplicate.
	globalRank2 := 2
	ranking.GlobalRank = &globalRank2
	if err := repo.SaveRanking(ctx, ranking); err != nil {
		t.Fatal(err)
	}
	got2, err := repo.FindRanking(ctx, spirit.ID, enrichment.SourceValveVRS)
	if err != nil {
		t.Fatal(err)
	}
	if got2.GlobalRank == nil || *got2.GlobalRank != 2 {
		t.Fatalf("expected the upsert to update GlobalRank to 2, got %v", got2.GlobalRank)
	}
}

func TestEnrichmentRepository_FindRankingsBatchesAcrossTeams(t *testing.T) {
	pool, ctx := newTestPool(t)
	catalog := pg.NewCompetitionRepository(pool)
	repo := pg.NewEnrichmentRepository(pool)

	event := competition.Event{ID: common.NewEventID(), Game: competition.GameCS2, Name: "E", ExternalID: "e1", Status: competition.EventUpcoming, Provider: "PANDASCORE"}
	if _, err := catalog.SaveEvent(ctx, event); err != nil {
		t.Fatal(err)
	}
	format, _ := competition.NewSeriesFormat(competition.BestOf, 3)
	a := competition.Team{ID: common.NewTeamID(), Name: "A", ExternalID: "ta"}
	b := competition.Team{ID: common.NewTeamID(), Name: "B", ExternalID: "tb"}
	match := competition.Match{ID: common.NewMatchID(), EventID: event.ID, ExternalID: "m1", Status: competition.MatchNotStarted, Format: format, FirstTeam: &a, SecondTeam: &b}
	if _, err := catalog.SaveMatch(ctx, match); err != nil {
		t.Fatal(err)
	}

	rankA, rankB := 1, 2
	published := timeMustParse(t, "2026-08-03")
	if err := repo.SaveRanking(ctx, enrichment.TeamRanking{TeamID: a.ID, GlobalRank: &rankA, PublishedAt: published, Source: enrichment.SourceValveVRS}); err != nil {
		t.Fatal(err)
	}
	if err := repo.SaveRanking(ctx, enrichment.TeamRanking{TeamID: b.ID, GlobalRank: &rankB, PublishedAt: published, Source: enrichment.SourceValveVRS}); err != nil {
		t.Fatal(err)
	}

	got, err := repo.FindRankings(ctx, []common.TeamID{a.ID, b.ID}, enrichment.SourceValveVRS)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[a.ID].GlobalRank == nil || *got[a.ID].GlobalRank != 1 || got[b.ID].GlobalRank == nil || *got[b.ID].GlobalRank != 2 {
		t.Fatalf("FindRankings = %+v, want both teams with GlobalRank 1 and 2", got)
	}

	empty, err := repo.FindRankings(ctx, nil, enrichment.SourceValveVRS)
	if err != nil || len(empty) != 0 {
		t.Fatalf("FindRankings(nil) = %+v, %v, want empty/nil", empty, err)
	}
}

func TestEnrichmentRepository_IdentitySaveFindAndAliasesRoundTrip(t *testing.T) {
	pool, ctx := newTestPool(t)
	catalog := pg.NewCompetitionRepository(pool)
	repo := pg.NewEnrichmentRepository(pool)

	event := competition.Event{ID: common.NewEventID(), Game: competition.GameCS2, Name: "E", ExternalID: "e1", Status: competition.EventUpcoming, Provider: "PANDASCORE"}
	if _, err := catalog.SaveEvent(ctx, event); err != nil {
		t.Fatal(err)
	}
	format, _ := competition.NewSeriesFormat(competition.BestOf, 3)
	navi := competition.Team{ID: common.NewTeamID(), Name: "Natus Vincere", ExternalID: "t1"}
	match := competition.Match{ID: common.NewMatchID(), EventID: event.ID, ExternalID: "m1", Status: competition.MatchNotStarted, Format: format, FirstTeam: &navi}
	if _, err := catalog.SaveMatch(ctx, match); err != nil {
		t.Fatal(err)
	}

	// No mapping yet.
	id, err := repo.FindTeamByExternalID(ctx, enrichment.SourceValveVRS, "navi-external-id")
	if err != nil || id != nil {
		t.Fatalf("FindTeamByExternalID before SaveIdentity = %v, %v, want nil, nil", id, err)
	}

	if err := repo.SaveIdentity(ctx, navi.ID, enrichment.SourceValveVRS, "navi-external-id", "NAVI", enrichment.ConfidenceExactName); err != nil {
		t.Fatal(err)
	}

	got, err := repo.FindTeamByExternalID(ctx, enrichment.SourceValveVRS, "navi-external-id")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || *got != navi.ID {
		t.Fatalf("FindTeamByExternalID = %v, want %v", got, navi.ID)
	}

	aliases, err := repo.Aliases(ctx, navi.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(aliases) != 1 || aliases[0] != "NAVI" {
		t.Fatalf("Aliases = %v, want [NAVI] (SaveIdentity should record the external name as an alias)", aliases)
	}

	// Saving again with the same external name must not duplicate the alias.
	if err := repo.SaveIdentity(ctx, navi.ID, enrichment.SourceValveVRS, "navi-external-id", "NAVI", enrichment.ConfidenceExactName); err != nil {
		t.Fatal(err)
	}
	aliases2, err := repo.Aliases(ctx, navi.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(aliases2) != 1 {
		t.Fatalf("Aliases after re-saving the same identity = %v, want still just [NAVI]", aliases2)
	}
}

func TestEnrichmentRepository_SyncStateTracksSuccessAndFailure(t *testing.T) {
	pool, ctx := newTestPool(t)
	repo := pg.NewEnrichmentRepository(pool)

	// Never synced yet: zero-value state, not an error.
	st, err := repo.State(ctx, enrichment.SourceValveVRS)
	if err != nil {
		t.Fatal(err)
	}
	if st.LastSuccessAt != nil || st.ConsecutiveFailures != 0 {
		t.Fatalf("initial state = %+v, want zero-value", st)
	}

	if err := repo.RecordFailure(ctx, enrichment.SourceValveVRS, "boom 1"); err != nil {
		t.Fatal(err)
	}
	if err := repo.RecordFailure(ctx, enrichment.SourceValveVRS, "boom 2"); err != nil {
		t.Fatal(err)
	}
	st, err = repo.State(ctx, enrichment.SourceValveVRS)
	if err != nil {
		t.Fatal(err)
	}
	if st.ConsecutiveFailures != 2 || st.LastError != "boom 2" {
		t.Fatalf("state after 2 failures = %+v, want ConsecutiveFailures=2 LastError=\"boom 2\"", st)
	}

	if err := repo.RecordSuccess(ctx, enrichment.SourceValveVRS); err != nil {
		t.Fatal(err)
	}
	st, err = repo.State(ctx, enrichment.SourceValveVRS)
	if err != nil {
		t.Fatal(err)
	}
	if st.ConsecutiveFailures != 0 || st.LastSuccessAt == nil {
		t.Fatalf("state after success = %+v, want ConsecutiveFailures reset to 0 and LastSuccessAt set", st)
	}
}

func TestEnrichmentRepository_SaveAndFindFormRoundTrips(t *testing.T) {
	pool, ctx := newTestPool(t)
	catalog := pg.NewCompetitionRepository(pool)
	repo := pg.NewEnrichmentRepository(pool)

	event := competition.Event{ID: common.NewEventID(), Game: competition.GameCS2, Name: "E", ExternalID: "e1", Status: competition.EventUpcoming, Provider: "PANDASCORE"}
	if _, err := catalog.SaveEvent(ctx, event); err != nil {
		t.Fatal(err)
	}
	format, _ := competition.NewSeriesFormat(competition.BestOf, 3)
	spirit := competition.Team{ID: common.NewTeamID(), Name: "Spirit", ExternalID: "t1"}
	match := competition.Match{ID: common.NewMatchID(), EventID: event.ID, ExternalID: "m1", Status: competition.MatchNotStarted, Format: format, FirstTeam: &spirit}
	if _, err := catalog.SaveMatch(ctx, match); err != nil {
		t.Fatal(err)
	}

	missing, err := repo.FindForm(ctx, spirit.ID, enrichment.SourceGRID)
	if err != nil || missing != nil {
		t.Fatalf("FindForm before any save = %v, %v, want nil, nil", missing, err)
	}

	if err := repo.SaveForm(ctx, spirit.ID, enrichment.RecentForm{Wins: 4, Losses: 1, Sample: 5, Source: enrichment.SourceGRID}); err != nil {
		t.Fatal(err)
	}
	got, err := repo.FindForm(ctx, spirit.ID, enrichment.SourceGRID)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Wins != 4 || got.Losses != 1 || got.Sample != 5 {
		t.Fatalf("FindForm = %+v, want Wins=4 Losses=1 Sample=5", got)
	}

	// Saving again must upsert, not duplicate.
	if err := repo.SaveForm(ctx, spirit.ID, enrichment.RecentForm{Wins: 3, Losses: 2, Sample: 5, Source: enrichment.SourceGRID}); err != nil {
		t.Fatal(err)
	}
	got2, err := repo.FindForm(ctx, spirit.ID, enrichment.SourceGRID)
	if err != nil {
		t.Fatal(err)
	}
	if got2.Wins != 3 || got2.Losses != 2 {
		t.Fatalf("expected the upsert to update Wins/Losses to 3/2, got %+v", got2)
	}
}

func TestEnrichmentRepository_HeadToHeadIsOrderIndependent(t *testing.T) {
	pool, ctx := newTestPool(t)
	catalog := pg.NewCompetitionRepository(pool)
	repo := pg.NewEnrichmentRepository(pool)

	event := competition.Event{ID: common.NewEventID(), Game: competition.GameCS2, Name: "E", ExternalID: "e1", Status: competition.EventUpcoming, Provider: "PANDASCORE"}
	if _, err := catalog.SaveEvent(ctx, event); err != nil {
		t.Fatal(err)
	}
	format, _ := competition.NewSeriesFormat(competition.BestOf, 3)
	a := competition.Team{ID: common.NewTeamID(), Name: "A", ExternalID: "ta"}
	b := competition.Team{ID: common.NewTeamID(), Name: "B", ExternalID: "tb"}
	match := competition.Match{ID: common.NewMatchID(), EventID: event.ID, ExternalID: "m1", Status: competition.MatchNotStarted, Format: format, FirstTeam: &a, SecondTeam: &b}
	if _, err := catalog.SaveMatch(ctx, match); err != nil {
		t.Fatal(err)
	}

	if err := repo.SaveHeadToHead(ctx, a.ID, b.ID, enrichment.HeadToHead{TeamAWins: 3, TeamBWins: 1, Sample: 4, Source: enrichment.SourceGRID}); err != nil {
		t.Fatal(err)
	}

	gotAB, err := repo.FindHeadToHead(ctx, a.ID, b.ID, enrichment.SourceGRID)
	if err != nil {
		t.Fatal(err)
	}
	if gotAB == nil || gotAB.TeamAWins != 3 || gotAB.TeamBWins != 1 {
		t.Fatalf("FindHeadToHead(a, b) = %+v, want TeamAWins=3 TeamBWins=1", gotAB)
	}

	// The reverse lookup order must report the record from the caller's own
	// perspective, not the stored (canonicalized) order.
	gotBA, err := repo.FindHeadToHead(ctx, b.ID, a.ID, enrichment.SourceGRID)
	if err != nil {
		t.Fatal(err)
	}
	if gotBA == nil || gotBA.TeamAWins != 1 || gotBA.TeamBWins != 3 {
		t.Fatalf("FindHeadToHead(b, a) = %+v, want TeamAWins=1 TeamBWins=3 (swapped)", gotBA)
	}

	// Saving from the reverse order must update the same row, not create a
	// second one — read back through either order and see the new values.
	if err := repo.SaveHeadToHead(ctx, b.ID, a.ID, enrichment.HeadToHead{TeamAWins: 2, TeamBWins: 5, Sample: 7, Source: enrichment.SourceGRID}); err != nil {
		t.Fatal(err)
	}
	gotAB2, err := repo.FindHeadToHead(ctx, a.ID, b.ID, enrichment.SourceGRID)
	if err != nil {
		t.Fatal(err)
	}
	if gotAB2 == nil || gotAB2.TeamAWins != 5 || gotAB2.TeamBWins != 2 || gotAB2.Sample != 7 {
		t.Fatalf("FindHeadToHead(a, b) after reverse-order save = %+v, want TeamAWins=5 TeamBWins=2 Sample=7", gotAB2)
	}
}

func TestEnrichmentRepository_SaveAndFindTournamentMetadataRoundTrips(t *testing.T) {
	pool, ctx := newTestPool(t)
	catalog := pg.NewCompetitionRepository(pool)
	repo := pg.NewEnrichmentRepository(pool)

	event := competition.Event{ID: common.NewEventID(), Game: competition.GameCS2, Name: "E", ExternalID: "e1", Status: competition.EventUpcoming, Provider: "PANDASCORE"}
	if _, err := catalog.SaveEvent(ctx, event); err != nil {
		t.Fatal(err)
	}

	missing, err := repo.FindTournamentMetadata(ctx, event.ID, enrichment.SourceLiquipedia)
	if err != nil || missing != nil {
		t.Fatalf("FindTournamentMetadata before any save = %v, %v, want nil, nil", missing, err)
	}

	meta := enrichment.TournamentMetadata{FullName: "CCT Europe Series #8 2026", Series: "CCT Europe Series", Region: "Europe", Stage: "Group Stage"}
	if err := repo.SaveTournamentMetadata(ctx, event.ID, meta, enrichment.SourceLiquipedia); err != nil {
		t.Fatal(err)
	}
	got, err := repo.FindTournamentMetadata(ctx, event.ID, enrichment.SourceLiquipedia)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || *got != meta {
		t.Fatalf("FindTournamentMetadata = %+v, want %+v", got, meta)
	}

	// Saving again must upsert, not duplicate.
	meta2 := enrichment.TournamentMetadata{FullName: "CCT Europe Series #8 2026", Series: "CCT Europe Series", Region: "Europe", Stage: "Playoffs"}
	if err := repo.SaveTournamentMetadata(ctx, event.ID, meta2, enrichment.SourceLiquipedia); err != nil {
		t.Fatal(err)
	}
	got2, err := repo.FindTournamentMetadata(ctx, event.ID, enrichment.SourceLiquipedia)
	if err != nil {
		t.Fatal(err)
	}
	if got2 == nil || got2.Stage != "Playoffs" {
		t.Fatalf("expected the upsert to update Stage to Playoffs, got %+v", got2)
	}
}
