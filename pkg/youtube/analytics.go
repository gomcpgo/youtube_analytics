package youtube

import (
	"context"
	"net/url"
	"strconv"
	"strings"
)

// Query is a YouTube Analytics API v2 targeted query against the
// authorized channel (ids=channel==MINE). Dates are YYYY-MM-DD, Pacific time.
type Query struct {
	Start, End string
	Metrics    []string
	Dimensions []string
	Filters    []string // joined with ';'
	Sort       string
	MaxResults int
	Currency   string
}

// Column describes one result column.
type Column struct {
	Name     string `json:"name"`
	Type     string `json:"columnType"`
	DataType string `json:"dataType"`
}

// Table is an Analytics API result table.
type Table struct {
	Columns []Column        `json:"columnHeaders"`
	Rows    [][]interface{} `json:"rows"`
}

// Query runs a targeted analytics query.
func (c *Client) Query(ctx context.Context, q Query) (*Table, error) {
	v := url.Values{
		"ids":       {"channel==MINE"},
		"startDate": {q.Start},
		"endDate":   {q.End},
		"metrics":   {strings.Join(q.Metrics, ",")},
	}
	if len(q.Dimensions) > 0 {
		v.Set("dimensions", strings.Join(q.Dimensions, ","))
	}
	if len(q.Filters) > 0 {
		v.Set("filters", strings.Join(q.Filters, ";"))
	}
	if q.Sort != "" {
		v.Set("sort", q.Sort)
	}
	if q.MaxResults > 0 {
		v.Set("maxResults", strconv.Itoa(q.MaxResults))
	}
	if q.Currency != "" {
		v.Set("currency", q.Currency)
	}
	var t Table
	if err := c.getJSON(ctx, withQuery(c.AnalyticsURL+"/reports", v), &t); err != nil {
		return nil, err
	}
	return &t, nil
}

// Index returns the column index for name, or -1.
func (t *Table) Index(name string) int {
	for i, c := range t.Columns {
		if c.Name == name {
			return i
		}
	}
	return -1
}

// Num reads a numeric cell; missing columns read as 0.
func (t *Table) Num(row []interface{}, name string) float64 {
	i := t.Index(name)
	if i < 0 || i >= len(row) {
		return 0
	}
	switch v := row[i].(type) {
	case float64:
		return v
	case string:
		f, _ := strconv.ParseFloat(v, 64)
		return f
	}
	return 0
}

// Str reads a dimension cell.
func (t *Table) Str(row []interface{}, name string) string {
	i := t.Index(name)
	if i < 0 || i >= len(row) {
		return ""
	}
	switch v := row[i].(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	return ""
}

// Records returns rows as column-name maps (for structured output).
func (t *Table) Records() []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(t.Rows))
	for _, r := range t.Rows {
		m := make(map[string]interface{}, len(t.Columns))
		for i, c := range t.Columns {
			if i < len(r) {
				m[c.Name] = r[i]
			}
		}
		out = append(out, m)
	}
	return out
}
