package xmltv

import (
	"os"
	"testing"
	"time"
)

func TestParseStandard(t *testing.T) {
	f, err := os.Open("testdata/standard.xml")
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer f.Close()

	var channels []Channel
	var programmes []Programme
	err = Parse(f,
		func(c Channel) error { channels = append(channels, c); return nil },
		func(p Programme) error { programmes = append(programmes, p); return nil },
	)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if len(channels) != 1 {
		t.Fatalf("want 1 channel, got %d", len(channels))
	}
	ch := channels[0]
	if ch.ID != "bbc1.uk" {
		t.Errorf("channel id = %q, want bbc1.uk", ch.ID)
	}
	if ch.DisplayName != "BBC One" {
		t.Errorf("channel display-name = %q, want BBC One", ch.DisplayName)
	}
	if ch.IconURL != "https://example.com/bbc1.png" {
		t.Errorf("channel icon = %q", ch.IconURL)
	}

	if len(programmes) != 1 {
		t.Fatalf("want 1 programme, got %d", len(programmes))
	}
	p := programmes[0]

	wantStart := time.Date(2026, 5, 21, 19, 0, 0, 0, time.UTC)
	if !p.Start.Equal(wantStart) {
		t.Errorf("start = %s, want %s", p.Start, wantStart)
	}
	if p.Start.Location() != time.UTC {
		t.Errorf("start location = %v, want UTC", p.Start.Location())
	}
	wantStop := time.Date(2026, 5, 21, 20, 0, 0, 0, time.UTC)
	if !p.Stop.Equal(wantStop) {
		t.Errorf("stop = %s, want %s", p.Stop, wantStop)
	}

	if p.Title != "News at Seven" {
		t.Errorf("title = %q", p.Title)
	}
	if p.SubTitle != "Top Stories" {
		t.Errorf("sub-title = %q", p.SubTitle)
	}
	if p.Description != "Headlines and weather." {
		t.Errorf("desc = %q", p.Description)
	}

	if p.SeasonNum == nil || *p.SeasonNum != 4 {
		t.Errorf("season = %v, want 4", p.SeasonNum)
	}
	if p.Episode == nil || *p.Episode != 5 {
		t.Errorf("episode = %v, want 5", p.Episode)
	}

	if len(p.Categories) != 2 || p.Categories[0] != "News" || p.Categories[1] != "Weather" {
		t.Errorf("categories = %v, want [News Weather]", p.Categories)
	}

	if len(p.Credits) != 2 {
		t.Fatalf("want 2 credits, got %d", len(p.Credits))
	}
	if p.Credits[0].Kind != "presenter" || p.Credits[0].Name != "Alex Doe" || p.Credits[0].Pos != 1 {
		t.Errorf("credit 0 = %+v, want presenter Alex Doe pos=1", p.Credits[0])
	}
	if p.Credits[1].Kind != "actor" || p.Credits[1].Name != "Sam Roe" || p.Credits[1].Pos != 2 {
		t.Errorf("credit 1 = %+v, want actor Sam Roe pos=2", p.Credits[1])
	}

	if p.Rating != "PG" {
		t.Errorf("rating = %q, want PG", p.Rating)
	}
	if p.IconURL != "https://example.com/prog.png" {
		t.Errorf("icon = %q", p.IconURL)
	}

	if p.OriginalAirDate == nil {
		t.Fatalf("original air date is nil")
	}
	wantAir := time.Date(2026, 5, 21, 0, 0, 0, 0, time.UTC)
	if !p.OriginalAirDate.Equal(wantAir) {
		t.Errorf("original air date = %s, want %s", p.OriginalAirDate, wantAir)
	}
}

func TestParseAutoGzip(t *testing.T) {
	f, err := os.Open("testdata/standard.xml.gz")
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer f.Close()

	var channels, programmes int
	err = ParseAuto(f,
		func(Channel) error { channels++; return nil },
		func(Programme) error { programmes++; return nil },
	)
	if err != nil {
		t.Fatalf("ParseAuto: %v", err)
	}
	if channels != 1 {
		t.Errorf("channel callbacks = %d, want 1", channels)
	}
	if programmes != 1 {
		t.Errorf("programme callbacks = %d, want 1", programmes)
	}
}
