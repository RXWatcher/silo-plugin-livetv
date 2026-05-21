// Package xmltv parses XMLTV-format EPG documents. The parser streams the
// document with encoding/xml so that arbitrarily large guides can be processed
// without buffering the whole tree in memory. Callers receive each channel and
// programme through callbacks.
package xmltv

import (
	"bufio"
	"compress/gzip"
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// Channel is one channel record from an XMLTV document.
type Channel struct {
	ID          string
	DisplayName string
	IconURL     string
}

// Credit is a single person credit on a programme.
type Credit struct {
	// Kind is one of actor, director, writer, presenter, guest, producer,
	// composer, editor.
	Kind string
	Name string
	// Pos is the running position across all credits on the programme,
	// preserving XML order.
	Pos int
}

// Programme is one programme record from an XMLTV document with the fields the
// Live TV plugin cares about extracted into typed values.
type Programme struct {
	Channel         string
	Start           time.Time // UTC
	Stop            time.Time // UTC
	Title           string
	SubTitle        string
	Description     string
	Categories      []string
	EpisodeNum      string
	SeasonNum       *int // 1-indexed when present
	Episode         *int // 1-indexed when present
	Rating          string
	IconURL         string
	OriginalAirDate *time.Time
	Credits         []Credit
}

// xmltvTimeLayout is the XMLTV time attribute format.
const xmltvTimeLayout = "20060102150405 -0700"

// Parse streams XMLTV, invoking onChannel once per <channel> and onProgramme
// once per <programme>. Either callback may be nil. A non-nil error returned by
// a callback halts parsing and is returned to the caller.
func Parse(r io.Reader, onChannel func(Channel) error, onProgramme func(Programme) error) error {
	dec := xml.NewDecoder(r)
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("xmltv: token: %w", err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch se.Name.Local {
		case "channel":
			var raw rawChannel
			if err := dec.DecodeElement(&raw, &se); err != nil {
				return fmt.Errorf("xmltv: decode channel: %w", err)
			}
			if onChannel != nil {
				if err := onChannel(raw.toChannel()); err != nil {
					return err
				}
			}
		case "programme":
			var raw rawProgramme
			if err := dec.DecodeElement(&raw, &se); err != nil {
				return fmt.Errorf("xmltv: decode programme: %w", err)
			}
			if onProgramme != nil {
				p, err := raw.toProgramme()
				if err != nil {
					return err
				}
				if err := onProgramme(p); err != nil {
					return err
				}
			}
		}
	}
}

// gzipMagic is the two-byte identifier that begins every gzip stream.
var gzipMagic = []byte{0x1f, 0x8b}

// ParseAuto wraps Parse and transparently decompresses gzip when the input
// begins with the gzip magic bytes.
func ParseAuto(r io.Reader, onChannel func(Channel) error, onProgramme func(Programme) error) error {
	br := bufio.NewReader(r)
	peek, _ := br.Peek(len(gzipMagic))
	if len(peek) == len(gzipMagic) && peek[0] == gzipMagic[0] && peek[1] == gzipMagic[1] {
		gz, err := gzip.NewReader(br)
		if err != nil {
			return fmt.Errorf("xmltv: gzip: %w", err)
		}
		defer gz.Close()
		return Parse(gz, onChannel, onProgramme)
	}
	return Parse(br, onChannel, onProgramme)
}

// --- raw XML structs ---

type rawIcon struct {
	Src string `xml:"src,attr"`
}

type rawChannel struct {
	ID          string  `xml:"id,attr"`
	DisplayName string  `xml:"display-name"`
	Icon        rawIcon `xml:"icon"`
}

func (c rawChannel) toChannel() Channel {
	return Channel{
		ID:          c.ID,
		DisplayName: strings.TrimSpace(c.DisplayName),
		IconURL:     c.Icon.Src,
	}
}

type rawEpisodeNum struct {
	System string `xml:"system,attr"`
	Value  string `xml:",chardata"`
}

type rawRating struct {
	System string `xml:"system,attr"`
	Value  string `xml:"value"`
}

// rawCredits captures credits children in source order via InnerXML reparsing.
type rawCredits struct {
	InnerXML string `xml:",innerxml"`
}

type rawProgramme struct {
	Channel     string          `xml:"channel,attr"`
	Start       string          `xml:"start,attr"`
	Stop        string          `xml:"stop,attr"`
	Title       string          `xml:"title"`
	SubTitle    string          `xml:"sub-title"`
	Description string          `xml:"desc"`
	Categories  []string        `xml:"category"`
	EpisodeNums []rawEpisodeNum `xml:"episode-num"`
	Rating      rawRating       `xml:"rating"`
	Icon        rawIcon         `xml:"icon"`
	Date        string          `xml:"date"`
	Credits     rawCredits      `xml:"credits"`
}

// creditKinds is the ordered set of credit child element names we capture.
// Order here is the priority used for assigning Pos when iterating innerxml.
var creditKinds = []string{
	"presenter",
	"director",
	"actor",
	"writer",
	"guest",
	"producer",
	"composer",
	"editor",
}

func isCreditKind(name string) bool {
	for _, k := range creditKinds {
		if k == name {
			return true
		}
	}
	return false
}

func (p rawProgramme) toProgramme() (Programme, error) {
	out := Programme{
		Channel:     p.Channel,
		Title:       strings.TrimSpace(p.Title),
		SubTitle:    strings.TrimSpace(p.SubTitle),
		Description: strings.TrimSpace(p.Description),
		Categories:  p.Categories,
		Rating:      strings.TrimSpace(p.Rating.Value),
		IconURL:     p.Icon.Src,
	}
	if p.Start != "" {
		t, err := time.Parse(xmltvTimeLayout, p.Start)
		if err != nil {
			return Programme{}, fmt.Errorf("xmltv: parse start: %w", err)
		}
		out.Start = t.UTC()
	}
	if p.Stop != "" {
		t, err := time.Parse(xmltvTimeLayout, p.Stop)
		if err != nil {
			return Programme{}, fmt.Errorf("xmltv: parse stop: %w", err)
		}
		out.Stop = t.UTC()
	}

	// Pick the xmltv_ns episode-num for season/episode and remember its raw value.
	for _, en := range p.EpisodeNums {
		if en.System == "xmltv_ns" {
			out.EpisodeNum = strings.TrimSpace(en.Value)
			s, e := parseXMLTVNS(out.EpisodeNum)
			out.SeasonNum = s
			out.Episode = e
			break
		}
	}

	if p.Date != "" {
		if t, err := time.Parse("20060102", strings.TrimSpace(p.Date)); err == nil {
			out.OriginalAirDate = &t
		}
	}

	if strings.TrimSpace(p.Credits.InnerXML) != "" {
		credits, err := parseCredits(p.Credits.InnerXML)
		if err != nil {
			return Programme{}, err
		}
		out.Credits = credits
	}

	return out, nil
}

// parseXMLTVNS converts an xmltv_ns episode-num value (dot-separated, 0-indexed,
// possibly with a `part/total` suffix) into 1-indexed season + episode pointers.
// Either component may be empty, in which case its returned pointer is nil.
func parseXMLTVNS(s string) (*int, *int) {
	if s == "" {
		return nil, nil
	}
	parts := strings.SplitN(s, ".", 3)
	parse := func(p string) *int {
		p = strings.TrimSpace(p)
		if p == "" {
			return nil
		}
		// Strip a "/total" suffix if present (e.g. "0/1").
		if slash := strings.Index(p, "/"); slash >= 0 {
			p = p[:slash]
		}
		p = strings.TrimSpace(p)
		if p == "" {
			return nil
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil
		}
		n++ // convert 0-indexed to 1-indexed
		return &n
	}
	var season, episode *int
	if len(parts) > 0 {
		season = parse(parts[0])
	}
	if len(parts) > 1 {
		episode = parse(parts[1])
	}
	return season, episode
}

// parseCredits walks the InnerXML of a <credits> element and emits Credit
// records in source order. Only known credit kinds are captured.
func parseCredits(inner string) ([]Credit, error) {
	dec := xml.NewDecoder(strings.NewReader(inner))
	var out []Credit
	pos := 0
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("xmltv: parse credits: %w", err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		kind := se.Name.Local
		if !isCreditKind(kind) {
			// Skip unknown child element entirely.
			if err := dec.Skip(); err != nil {
				return nil, fmt.Errorf("xmltv: skip credit: %w", err)
			}
			continue
		}
		var name string
		if err := dec.DecodeElement(&name, &se); err != nil {
			return nil, fmt.Errorf("xmltv: decode credit: %w", err)
		}
		pos++
		out = append(out, Credit{
			Kind: kind,
			Name: strings.TrimSpace(name),
			Pos:  pos,
		})
	}
	return out, nil
}
