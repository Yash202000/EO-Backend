package services

// Filters, sorting and paging for the ticket list APIs.

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/automax/backend/internal/models"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

var handleRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// ticketSorts maps the sort parameter to a column.
var ticketSorts = map[string]string{
	"created_at": "t.created_at",
	"updated_at": "t.updated_at",
	"due_date":   "t.due_date",
	"reference":  "t.reference",
	"title":      "t.title",
}

// SplitList turns "a, b,,c" into [a b c].
func SplitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// dayRange adds "col >= from" and "col < to + 1 day" for YYYY-MM-DD values;
// fromName and toName are the query parameters, for the error message.
func dayRange(q *gorm.DB, col, from, to, fromName, toName string) (*gorm.DB, error) {
	if from != "" {
		d, err := time.Parse("2006-01-02", from)
		if err != nil {
			return nil, fmt.Errorf("%s must be YYYY-MM-DD", fromName)
		}
		q = q.Where(col+" >= ?", d)
	}
	if to != "" {
		d, err := time.Parse("2006-01-02", to)
		if err != nil {
			return nil, fmt.Errorf("%s must be YYYY-MM-DD", toName)
		}
		q = q.Where(col+" < ?", d.AddDate(0, 0, 1))
	}
	return q, nil
}

func checkUUID(name, v string) error {
	if v == "" {
		return nil
	}
	if _, err := uuid.Parse(v); err != nil {
		return fmt.Errorf("%s must be a uuid", name)
	}
	return nil
}

// NormalisePaging applies the list defaults: page 1, limit 20, at most 100.
func NormalisePaging(f *models.TicketFilter) {
	if f.Page < 1 {
		f.Page = 1
	}
	if f.Limit < 1 {
		f.Limit = 20
	}
	if f.Limit > 100 {
		f.Limit = 100
	}
}

// attrValues turns one attribute filter value into the texts to match:
// a scalar gives one, a list gives each of its items.
func attrValues(v interface{}) ([]string, bool) {
	switch x := v.(type) {
	case string:
		return []string{x}, true
	case float64, bool:
		return []string{fmt.Sprint(x)}, true
	case []interface{}:
		out := []string{}
		for _, item := range x {
			one, ok := attrValues(item)
			if !ok || len(one) != 1 {
				return nil, false
			}
			out = append(out, one[0])
		}
		return out, len(out) > 0
	}
	return nil, false
}

// ListTickets returns one page of tickets matching the filter.
func (s *TicketService) ListTickets(ctx context.Context, f *models.TicketFilter) ([]models.TicketListRow, int64, error) {
	NormalisePaging(f)
	for name, v := range map[string]string{"parent_id": f.ParentID, "root_id": f.RootID, "created_by": f.CreatedBy} {
		if err := checkUUID(name, v); err != nil {
			return nil, 0, err
		}
	}

	q := s.db.WithContext(ctx).Table("tickets t").
		Joins("JOIN ticket_types tt ON tt.id = t.ticket_type_id").
		Joins("JOIN workflow_states ws ON ws.id = t.current_state_id").
		Joins("LEFT JOIN users u ON u.id = t.created_by_id").
		Where("t.deleted_at IS NULL")

	if len(f.TypeCodes) > 0 {
		codes := make([]string, len(f.TypeCodes))
		for i, c := range f.TypeCodes {
			codes[i] = strings.ToUpper(strings.TrimSpace(c))
		}
		q = q.Where("tt.code IN ?", codes)
	}
	if len(f.Levels) > 0 {
		for _, l := range f.Levels {
			switch l {
			case models.TicketLevelMain, models.TicketLevelSub, models.TicketLevelSubSub, models.TicketLevelUniversal:
			default:
				return nil, 0, errors.New("levels must be main, sub, sub_sub or universal")
			}
		}
		q = q.Where("tt.level IN ?", f.Levels)
	}
	if f.TopLevel {
		q = q.Where("t.parent_id IS NULL")
	}
	if f.HasChildren {
		q = q.Where("EXISTS (SELECT 1 FROM tickets c WHERE c.parent_id = t.id AND c.deleted_at IS NULL)")
	}
	if f.ParentID != "" {
		q = q.Where("t.parent_id = ?", f.ParentID)
	}
	if f.RootID != "" {
		q = q.Where("t.root_id = ?", f.RootID)
	}
	if len(f.States) > 0 {
		q = q.Where("ws.code IN ?", f.States)
	}
	switch f.Status {
	case "":
	case "open":
		q = q.Where("t.closed_at IS NULL")
	case "closed":
		q = q.Where("t.closed_at IS NOT NULL")
	default:
		return nil, 0, errors.New("status must be open or closed")
	}
	if f.CreatedBy != "" {
		q = q.Where("t.created_by_id = ?", f.CreatedBy)
	}
	if f.Search != "" {
		like := "%" + strings.ToLower(f.Search) + "%"
		q = q.Where("(LOWER(t.reference) LIKE ? OR LOWER(t.title) LIKE ?)", like, like)
	}
	if f.Overdue {
		q = q.Where("t.closed_at IS NULL AND t.due_date < ?", time.Now())
	}
	var err error
	if q, err = dayRange(q, "t.due_date", f.DueFrom, f.DueTo, "due_from", "due_to"); err != nil {
		return nil, 0, err
	}
	if q, err = dayRange(q, "t.created_at", f.CreatedFrom, f.CreatedTo, "created_from", "created_to"); err != nil {
		return nil, 0, err
	}
	for handle, v := range f.Attributes {
		if !handleRe.MatchString(handle) {
			return nil, 0, fmt.Errorf("attributes.%s: not a valid field handle", handle)
		}
		values, ok := attrValues(v)
		if !ok {
			return nil, 0, fmt.Errorf("attributes.%s: must be text, a number, true/false, or a list of them", handle)
		}
		// Equal to any of the values, or (multi fields) a list containing one.
		var conds []string
		var args []interface{}
		for _, v := range values {
			conds = append(conds, "t.attributes ->> ?::text = ?::text OR jsonb_exists(t.attributes -> ?::text, ?::text)")
			args = append(args, handle, v, handle, v)
		}
		q = q.Where("("+strings.Join(conds, " OR ")+")", args...)
	}

	var total int64
	if err := q.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	sortCol := "t.created_at"
	if f.Sort != "" {
		col, ok := ticketSorts[f.Sort]
		if !ok {
			return nil, 0, errors.New("sort must be one of created_at, updated_at, due_date, reference, title")
		}
		sortCol = col
	}
	order := "DESC"
	switch strings.ToLower(f.Order) {
	case "", "desc":
	case "asc":
		order = "ASC"
	default:
		return nil, 0, errors.New("order must be asc or desc")
	}

	rows := []models.TicketListRow{}
	err = q.Select(`t.id, t.reference, t.title, tt.code AS type_code, tt.name AS type_name,
			ws.name AS state, ws.code AS state_code, t.parent_id, t.root_id, t.due_date, t.closed_at,
			` + userName + ` AS created_by, t.created_at, t.updated_at`).
		Order(sortCol + " " + order + " NULLS LAST, t.id").
		Offset((f.Page - 1) * f.Limit).Limit(f.Limit).
		Scan(&rows).Error
	return rows, total, err
}

// filterTypeRows keeps the ticket type rows that match the filter.
func filterTypeRows(rows []models.TicketTypeRow, f *models.TicketTypeFilter) []models.TicketTypeRow {
	statuses, levels := SplitList(f.Status), SplitList(f.Level)
	search := strings.ToLower(strings.TrimSpace(f.Search))
	out := []models.TicketTypeRow{}
	for _, r := range rows {
		if len(statuses) > 0 && !contains(statuses, r.Status) {
			continue
		}
		if len(levels) > 0 && !contains(levels, r.Level) {
			continue
		}
		if search != "" && !strings.Contains(strings.ToLower(r.Code+" "+r.Name), search) {
			continue
		}
		out = append(out, r)
	}
	return out
}
