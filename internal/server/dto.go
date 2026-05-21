package server

import (
	"time"

	"github.com/RXWatcher/continuum-plugin-livetv/internal/store"
)

// channelDTO is the wire shape returned by GET /channels and GET /channels/{id}.
// We deliberately collapse the (src, admin, effective) triplet down to a single
// "effective" value per field so SPA consumers don't have to reason about the
// override layering.
type channelDTO struct {
	ID             string      `json:"id"`
	DisplayName    string      `json:"display_name"`
	ChannelNumber  string      `json:"channel_number,omitempty"`
	GroupTitle     string      `json:"group_title,omitempty"`
	LogoURL        string      `json:"logo_url,omitempty"`
	UpstreamKind   string      `json:"upstream_kind,omitempty"`
	IsFavorite     bool        `json:"is_favorite"`
	CurrentProgram *programRef `json:"current_program,omitempty"`
	NextProgram    *programRef `json:"next_program,omitempty"`
}

// programRef is the thin now/next program shape returned alongside a channel.
type programRef struct {
	ID    string    `json:"id"`
	Title string    `json:"title"`
	Start time.Time `json:"start"`
	Stop  time.Time `json:"stop"`
}

// toChannelDTO collapses a store.Channel onto the wire shape.
func toChannelDTO(c store.Channel) channelDTO {
	out := channelDTO{
		ID:            c.ID,
		DisplayName:   c.DisplayName,
		ChannelNumber: c.EffectiveChannelNum,
		GroupTitle:    c.EffectiveGroupTitle,
		LogoURL:       c.LogoURL,
		UpstreamKind:  c.UpstreamKind,
		IsFavorite:    c.HasFavorite,
	}
	if c.CurrentProgram != nil {
		out.CurrentProgram = &programRef{
			ID:    c.CurrentProgram.ID,
			Title: c.CurrentProgram.Title,
			Start: c.CurrentProgram.Start,
			Stop:  c.CurrentProgram.Stop,
		}
	}
	if c.NextProgram != nil {
		out.NextProgram = &programRef{
			ID:    c.NextProgram.ID,
			Title: c.NextProgram.Title,
			Start: c.NextProgram.Start,
			Stop:  c.NextProgram.Stop,
		}
	}
	return out
}

// programDTO is the full program shape returned by GET /programs/{id},
// GET /programs/search, and GET /guide. Credits are nil for guide/search
// responses; only the detail handler hydrates them.
type programDTO struct {
	ID              string      `json:"id"`
	ChannelID       string      `json:"channel_id,omitempty"`
	XMLTVChannelID  string      `json:"xmltv_channel_id,omitempty"`
	Title           string      `json:"title"`
	SubTitle        string      `json:"sub_title,omitempty"`
	Description     string      `json:"description,omitempty"`
	EpisodeNum      string      `json:"episode_num,omitempty"`
	SeasonNum       *int        `json:"season_num,omitempty"`
	Episode         *int        `json:"episode,omitempty"`
	Categories      []string    `json:"categories,omitempty"`
	Rating          string      `json:"rating,omitempty"`
	IconURL         string      `json:"icon_url,omitempty"`
	Start           time.Time   `json:"start"`
	Stop            time.Time   `json:"stop"`
	OriginalAirDate *time.Time  `json:"original_air_date,omitempty"`
	Credits         []creditDTO `json:"credits,omitempty"`
}

// creditDTO is the wire shape for a program credit row.
type creditDTO struct {
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	Position int    `json:"position"`
}

// toProgramDTO collapses a store.Program onto the wire shape. channelID is the
// internal channel id when known (set by guide queries that join through
// channel_epg_keys); pass "" otherwise.
func toProgramDTO(p store.Program, channelID string) programDTO {
	out := programDTO{
		ID:              p.ID,
		ChannelID:       channelID,
		XMLTVChannelID:  p.XMLTVChannelID,
		Title:           p.Title,
		SubTitle:        p.SubTitle,
		Description:     p.Description,
		EpisodeNum:      p.EpisodeNum,
		SeasonNum:       p.SeasonNum,
		Episode:         p.Episode,
		Categories:      p.Categories,
		Rating:          p.Rating,
		IconURL:         p.IconURL,
		Start:           p.Start,
		Stop:            p.Stop,
		OriginalAirDate: p.OriginalAirDate,
	}
	if len(p.Credits) > 0 {
		out.Credits = make([]creditDTO, len(p.Credits))
		for i, c := range p.Credits {
			out.Credits[i] = creditDTO{Kind: c.Kind, Name: c.Name, Position: c.Position}
		}
	}
	return out
}

// favoriteDTO is one row of GET /favorites.
type favoriteDTO struct {
	ChannelID string `json:"channel_id"`
	Position  int    `json:"position"`
}

// recentDTO is one row of GET /recent.
type recentDTO struct {
	ChannelID   string    `json:"channel_id"`
	LastTunedAt time.Time `json:"last_tuned_at"`
}
