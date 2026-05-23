package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/RXWatcher/silo-plugin-livetv/internal/store"
	"github.com/RXWatcher/silo-plugin-livetv/internal/testutil"
)

func TestReplaceFutureForChannel(t *testing.T) {
	pool := testutil.StartPG(t)
	s := store.New(pool)
	ctx := context.Background()

	now := time.Now().UTC()
	past := now.Add(-2 * time.Hour)
	pastEnd := now.Add(-1 * time.Hour)
	future1 := now.Add(2 * time.Hour)
	future1End := future1.Add(30 * time.Minute)
	future2 := future1End.Add(0)
	future2End := future2.Add(30 * time.Minute)
	future3 := future2End.Add(0)
	future3End := future3.Add(30 * time.Minute)

	// Seed: one past program on chA, three future on chA, one future on chB.
	if err := s.ReplaceFutureForChannel(ctx, "chA", []store.Program{
		{Start: future1, Stop: future1End, Title: "A1"},
		{Start: future2, Stop: future2End, Title: "A2"},
		{Start: future3, Stop: future3End, Title: "A3"},
	}); err != nil {
		t.Fatalf("seed future chA: %v", err)
	}
	// Insert past chA manually since ReplaceFutureForChannel deletes future-only.
	if _, err := pool.Exec(ctx, `
		INSERT INTO programs (id, xmltv_channel_id, start_utc, stop_utc, title)
		VALUES ('pastA', 'chA', $1, $2, 'A0-past')
	`, past, pastEnd); err != nil {
		t.Fatalf("seed past: %v", err)
	}
	if err := s.ReplaceFutureForChannel(ctx, "chB", []store.Program{
		{Start: future1, Stop: future1End, Title: "B1"},
	}); err != nil {
		t.Fatalf("seed chB: %v", err)
	}

	// Replace chA future with 2 programs.
	if err := s.ReplaceFutureForChannel(ctx, "chA", []store.Program{
		{Start: future1, Stop: future1End, Title: "A1-new"},
		{Start: future2, Stop: future2End, Title: "A2-new"},
	}); err != nil {
		t.Fatalf("replace: %v", err)
	}

	// chA now has 1 past + 2 future = 3 rows; chB still has 1.
	var countA, countB, futureA int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM programs WHERE xmltv_channel_id='chA'`).Scan(&countA); err != nil {
		t.Fatalf("count A: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM programs WHERE xmltv_channel_id='chB'`).Scan(&countB); err != nil {
		t.Fatalf("count B: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM programs WHERE xmltv_channel_id='chA' AND start_utc >= now()`).Scan(&futureA); err != nil {
		t.Fatalf("count future A: %v", err)
	}
	if countA != 3 {
		t.Fatalf("chA total = %d, want 3 (1 past + 2 future)", countA)
	}
	if futureA != 2 {
		t.Fatalf("chA future = %d, want 2", futureA)
	}
	if countB != 1 {
		t.Fatalf("chB total = %d, want 1", countB)
	}

	// Confirm the past row survived intact.
	var pastTitle string
	if err := pool.QueryRow(ctx, `SELECT title FROM programs WHERE id='pastA'`).Scan(&pastTitle); err != nil {
		t.Fatalf("get past: %v", err)
	}
	if pastTitle != "A0-past" {
		t.Fatalf("past row mutated: %s", pastTitle)
	}
}

func TestPruneOldPrograms(t *testing.T) {
	pool := testutil.StartPG(t)
	s := store.New(pool)
	ctx := context.Background()

	now := time.Now().UTC()
	if _, err := pool.Exec(ctx, `
		INSERT INTO programs (id, xmltv_channel_id, start_utc, stop_utc, title) VALUES
		('old', 'c1', $1, $2, 'old'),
		('cur', 'c1', $3, $4, 'cur'),
		('new', 'c1', $5, $6, 'new')
	`,
		now.Add(-10*time.Hour), now.Add(-9*time.Hour),
		now.Add(-30*time.Minute), now.Add(30*time.Minute),
		now.Add(1*time.Hour), now.Add(2*time.Hour),
	); err != nil {
		t.Fatalf("seed: %v", err)
	}
	n, err := s.PruneOldPrograms(ctx, now.Add(-1*time.Hour))
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if n != 1 {
		t.Fatalf("pruned = %d, want 1", n)
	}
	var remaining int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM programs`).Scan(&remaining)
	if remaining != 2 {
		t.Fatalf("remaining = %d, want 2", remaining)
	}
}

func TestGuideWindowKeyedByChannelID(t *testing.T) {
	pool := testutil.StartPG(t)
	s := store.New(pool)
	ctx := context.Background()
	srcID := seedM3USource(t, ctx, s)
	chID, _ := s.UpsertChannelFromM3U(ctx, store.Channel{
		SourceM3UID: srcID, SourceChannelID: "x", DisplayName: "X", UpstreamURL: "u",
	})
	if err := s.AddEPGKey(ctx, chID, "xmltv.x", true); err != nil {
		t.Fatalf("add epg: %v", err)
	}

	now := time.Now().UTC()
	if err := s.ReplaceFutureForChannel(ctx, "xmltv.x", []store.Program{
		{Start: now.Add(1 * time.Hour), Stop: now.Add(2 * time.Hour), Title: "Soon"},
		{Start: now.Add(5 * time.Hour), Stop: now.Add(6 * time.Hour), Title: "Later"},
		{Start: now.Add(48 * time.Hour), Stop: now.Add(49 * time.Hour), Title: "Far"},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	guide, err := s.GuideWindow(ctx, []string{chID}, now, now.Add(12*time.Hour))
	if err != nil {
		t.Fatalf("guide: %v", err)
	}
	progs, ok := guide[chID]
	if !ok {
		t.Fatalf("guide missing channel id %s; got %v", chID, guide)
	}
	if len(progs) != 2 {
		t.Fatalf("progs in window = %d, want 2", len(progs))
	}
	if progs[0].Title != "Soon" || progs[1].Title != "Later" {
		t.Fatalf("progs order/title: %+v", progs)
	}
}

func TestGetProgramCreditsOrder(t *testing.T) {
	pool := testutil.StartPG(t)
	s := store.New(pool)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Second)
	if err := s.ReplaceFutureForChannel(ctx, "ch1", []store.Program{
		{ID: "prog1", Start: now.Add(time.Hour), Stop: now.Add(2 * time.Hour), Title: "Movie"},
	}); err != nil {
		t.Fatalf("insert program: %v", err)
	}
	credits := []store.Credit{
		{Kind: "director", Name: "Jane", Position: 0},
		{Kind: "actor", Name: "Ada", Position: 1},
		{Kind: "actor", Name: "Bob", Position: 2},
	}
	if err := s.InsertCredits(ctx, "prog1", credits); err != nil {
		t.Fatalf("insert credits: %v", err)
	}
	p, err := s.GetProgram(ctx, "prog1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(p.Credits) != 3 {
		t.Fatalf("credits len = %d", len(p.Credits))
	}
	for i, c := range p.Credits {
		if c.Position != i {
			t.Fatalf("credit %d position = %d, want %d (%+v)", i, c.Position, i, p.Credits)
		}
	}
}

func TestGetProgramNotFound(t *testing.T) {
	pool := testutil.StartPG(t)
	s := store.New(pool)
	ctx := context.Background()
	if _, err := s.GetProgram(ctx, "missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestSearchPrograms(t *testing.T) {
	pool := testutil.StartPG(t)
	s := store.New(pool)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Second)
	if err := s.ReplaceFutureForChannel(ctx, "ch1", []store.Program{
		{Start: now.Add(1 * time.Hour), Stop: now.Add(2 * time.Hour), Title: "Galactic News"},
		{Start: now.Add(3 * time.Hour), Stop: now.Add(4 * time.Hour), Title: "Cooking Hour", Description: "Galactic chefs compete"},
		{Start: now.Add(48 * time.Hour), Stop: now.Add(49 * time.Hour), Title: "Galactic Movie"},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	hits, err := s.SearchPrograms(ctx, "galactic", now, now.Add(12*time.Hour), 50)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("hits = %d, want 2 (excludes the 48h-out program)", len(hits))
	}
	if hits[0].Title != "Galactic News" {
		t.Fatalf("ordering wrong: %+v", hits)
	}

	// case insensitivity
	hitsUpper, _ := s.SearchPrograms(ctx, "GALACTIC", now, now.Add(12*time.Hour), 50)
	if len(hitsUpper) != 2 {
		t.Fatalf("case-insensitive search = %d, want 2", len(hitsUpper))
	}

	// sub_title search: insert a program whose only match is on sub_title.
	if err := s.ReplaceFutureForChannel(ctx, "ch2", []store.Program{
		{Start: now.Add(2 * time.Hour), Stop: now.Add(3 * time.Hour), Title: "Daily Show", SubTitle: "Galactic Special"},
	}); err != nil {
		t.Fatalf("seed sub_title: %v", err)
	}
	hitsSub, err := s.SearchPrograms(ctx, "galactic", now, now.Add(12*time.Hour), 50)
	if err != nil {
		t.Fatalf("search sub_title: %v", err)
	}
	if len(hitsSub) != 3 {
		t.Fatalf("sub_title match count = %d, want 3", len(hitsSub))
	}
}
