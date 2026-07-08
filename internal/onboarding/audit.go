package onboarding

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const auditLogName = "audit.jsonl"

type auditEvent struct {
	Time  time.Time      `json:"time"`
	Event string         `json:"event"`
	Data  map[string]any `json:"data,omitempty"`
}

func (m Manager) writeAudit(event string, data map[string]any) error {
	if event == "" {
		return nil
	}
	path := filepath.Join(m.logsDir(), auditLogName)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	line := auditEvent{
		Time:  m.now(),
		Event: event,
		Data:  sanitizeAuditData(data),
	}
	b, err := json.Marshal(line)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(append(b, '\n')); err != nil {
		return err
	}
	return nil
}

func sanitizeAuditData(data map[string]any) map[string]any {
	if len(data) == 0 {
		return nil
	}
	out := make(map[string]any, len(data))
	for key, value := range data {
		lower := strings.ToLower(key)
		if strings.Contains(lower, "private") || strings.Contains(lower, "key_pem") || strings.Contains(lower, "csr") || strings.Contains(lower, "rdp_content") {
			continue
		}
		out[key] = value
	}
	return out
}

func (m Manager) RecordCertificateRejected(nodeID, fingerprint, remoteAddr, reason string) error {
	return m.writeAudit("certificate_rejected", map[string]any{
		"node_id":     nodeID,
		"fingerprint": fingerprint,
		"remote_addr": remoteAddr,
		"reason":      reason,
	})
}
