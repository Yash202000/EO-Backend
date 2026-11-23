package services

// SMS template helpers. The Automax Amana (MOMRA) SMS gateway was removed;
// an EO SMS provider plugs in through SMSTemplateSender (NotificationService.SetSMSSender).

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// AmanaSMSResult is what Amana answered for one send; kept for the audit trail in notification_logs.
type AmanaSMSResult struct {
	MessageID  string
	StatusCode int
	Response   string // trimmed, secret-safe excerpt of the response body
}

// SMSTemplateSender sends an SMS through an Amana-hosted template.
type SMSTemplateSender interface {
	SendSMSByTemplateID(ctx context.Context, mobile string, templateID int, variables []string) (string, error)
	// SendSMSByTemplateIDDetailed is SendSMSByTemplateID plus the provider status/response for auditing.
	// The result is non-nil whenever Amana was actually reached, even if the call failed.
	SendSMSByTemplateIDDetailed(ctx context.Context, mobile string, templateID int, variables []string) (*AmanaSMSResult, error)
}

// smsLogIDKey carries the notification_logs row id of the current send so every log line can be correlated.
type smsLogIDKey struct{}

func withSMSLogID(ctx context.Context, id uuid.UUID) context.Context {
	return context.WithValue(ctx, smsLogIDKey{}, id)
}

// smsLogTag renders "log_id=<uuid>" for log lines ("-" outside a tracked send).
func smsLogTag(ctx context.Context) string {
	if id, ok := ctx.Value(smsLogIDKey{}).(uuid.UUID); ok {
		return "log_id=" + id.String()
	}
	return "log_id=-"
}

// ParseTemplateVariables turns a template's Variables column into an ordered list of names.
// Accepts a JSON array (["a","b"]) or a comma/semicolon/newline separated list; {{ }} wrappers are stripped.
func ParseTemplateVariables(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var names []string
	if strings.HasPrefix(raw, "[") && json.Unmarshal([]byte(raw), &names) != nil {
		names = nil
	}
	if names == nil {
		names = strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == '\n' || r == ';' })
	}
	out := make([]string, 0, len(names))
	for _, n := range names {
		n = strings.TrimSpace(n)
		n = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(n, "{{"), "}}"))
		n = strings.TrimPrefix(n, ".")
		if n != "" {
			out = append(out, n)
		}
	}
	return out
}

// BuildAmanaVariables orders values by the template's variable list and fails if any variable is
// missing (key absent). A key that is present with an empty value (e.g. an empty comment) is valid
// and is passed through as an empty string.
func BuildAmanaVariables(names []string, vars map[string]string) ([]string, error) {
	lower := make(map[string]string, len(vars))
	for k, v := range vars {
		lower[strings.ToLower(k)] = v
	}
	out := make([]string, 0, len(names))
	var missing []string
	for _, n := range names {
		v, ok := lower[strings.ToLower(n)]
		if !ok {
			missing = append(missing, n)
			continue
		}
		out = append(out, v)
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing required SMS template variables: %s", strings.Join(missing, ", "))
	}
	return out, nil
}

// ExtractPlaceholders lists {{placeholders}} of a template body in order of appearance
// (duplicates kept), used as the Amana variable order when the template has no explicit variables list.
func ExtractPlaceholders(body string) []string {
	matches := placeholderRe.FindAllStringSubmatch(body, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, m[1])
	}
	return out
}

// maskMobile keeps only the last 3 digits so logs never carry a full phone number.
func maskMobile(m string) string {
	if len(m) <= 3 {
		return "***"
	}
	return strings.Repeat("*", len(m)-3) + m[len(m)-3:]
}

// maskPhones applies maskMobile to each number for safe logging.
func maskPhones(phones []string) []string {
	out := make([]string, len(phones))
	for i, p := range phones {
		out[i] = maskMobile(p)
	}
	return out
}
