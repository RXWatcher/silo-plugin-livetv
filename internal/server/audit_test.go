package server

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hashicorp/go-hclog"

	"github.com/RXWatcher/silo-plugin-livetv/internal/streamproxy"
)

// TestAuditSourceMutation_RedactsSecrets verifies the audit line never carries
// raw upstream credentials: embedded userinfo, xtream-style query creds, and
// credential headers must all be masked, while the actor and action survive.
func TestAuditSourceMutation_RedactsSecrets(t *testing.T) {
	var buf bytes.Buffer
	log := hclog.New(&hclog.LoggerOptions{
		Name:       "audit",
		Output:     &buf,
		JSONFormat: true,
		Level:      hclog.Info,
	})
	s := &Server{AuditLogger: log}

	ctx := streamproxy.WithUserID(context.Background(), "admin-42")
	rawURL := "http://user:supersecret@host.example/get.php?username=joe&password=hunter2&type=m3u"
	headers := map[string]string{
		"Authorization": "Bearer topsecrettoken",
		"User-Agent":    "livetv/1.0",
	}
	s.auditSourceMutation(ctx, "update", "m3u", "src-1", rawURL, headers)

	out := buf.String()
	for _, leak := range []string{"supersecret", "hunter2", "topsecrettoken"} {
		if strings.Contains(out, leak) {
			t.Fatalf("audit log leaked secret %q: %s", leak, out)
		}
	}

	// Structured fields must still be present and useful.
	var rec map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &rec); err != nil {
		t.Fatalf("audit line not valid JSON: %v\n%s", err, out)
	}
	if rec["actor"] != "admin-42" {
		t.Errorf("actor = %v, want admin-42", rec["actor"])
	}
	if rec["action"] != "update" {
		t.Errorf("action = %v, want update", rec["action"])
	}
	if rec["source_kind"] != "m3u" || rec["source_id"] != "src-1" {
		t.Errorf("source fields = %v/%v, want m3u/src-1", rec["source_kind"], rec["source_id"])
	}
	if url, _ := rec["url"].(string); !strings.Contains(url, maskedValue) {
		t.Errorf("url not masked: %v", rec["url"])
	}
}

// TestAuditSourceMutation_DeleteOmitsURL confirms a delete (no URL/headers)
// logs cleanly without empty credential fields.
func TestAuditSourceMutation_DeleteOmitsURL(t *testing.T) {
	var buf bytes.Buffer
	log := hclog.New(&hclog.LoggerOptions{
		Name:       "audit",
		Output:     &buf,
		JSONFormat: true,
		Level:      hclog.Info,
	})
	s := &Server{AuditLogger: log}
	ctx := streamproxy.WithUserID(context.Background(), "admin-7")
	s.auditSourceMutation(ctx, "delete", "xmltv", "src-9", "", nil)

	var rec map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &rec); err != nil {
		t.Fatalf("audit line not valid JSON: %v", err)
	}
	if _, ok := rec["url"]; ok {
		t.Errorf("delete audit should omit url field, got %v", rec["url"])
	}
	if rec["action"] != "delete" || rec["actor"] != "admin-7" {
		t.Errorf("unexpected fields: %v", rec)
	}
}
