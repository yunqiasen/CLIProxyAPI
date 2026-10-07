// Package modelcatalog owns discovery-only model presentation.
package modelcatalog

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

type item struct {
	raw    json.RawMessage
	id     string
	gemini bool
	index  int
}

func parse(body []byte) (map[string]json.RawMessage, string, []item, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, "", nil, err
	}
	key := "data"
	if _, ok := root[key]; !ok {
		key = "models"
	}
	var rows []json.RawMessage
	raw, ok := root[key]
	raw = bytes.TrimSpace(raw)
	if !ok || len(raw) == 0 || raw[0] != '[' {
		return nil, "", nil, fmt.Errorf("unsupported model catalog")
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, "", nil, err
	}
	items := make([]item, 0, len(rows))
	for i, r := range rows {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(r, &fields); err != nil {
			return nil, "", nil, err
		}
		id, field := "", ""
		for _, f := range []string{"id", "slug", "name"} {
			if v, ok := fields[f]; ok {
				if json.Unmarshal(v, &id) != nil || id == "" {
					return nil, "", nil, fmt.Errorf("invalid catalog identifier")
				}
				field = f
				break
			}
		}
		if id == "" {
			return nil, "", nil, fmt.Errorf("missing catalog identifier")
		}
		items = append(items, item{raw: r, id: id, gemini: field == "name", index: i})
	}
	return root, key, items, nil
}

// match uses only '*' as a wildcard, matching zero or more bytes.
func match(pattern, value string) bool {
	p, v, star, retry := 0, 0, -1, 0
	for v < len(value) {
		if p < len(pattern) && pattern[p] != '*' && pattern[p] == value[v] {
			p++
			v++
			continue
		}
		if p < len(pattern) && pattern[p] == '*' {
			star = p
			p++
			retry = v
			continue
		}
		if star < 0 {
			return false
		}
		retry++
		v = retry
		p = star + 1
	}
	for p < len(pattern) && pattern[p] == '*' {
		p++
	}
	return p == len(pattern)
}

func matching(patterns []string, row item) []string {
	out := make([]string, 0)
	for _, pattern := range patterns {
		if match(pattern, row.id) || (row.gemini && match(pattern, strings.TrimPrefix(row.id, "models/"))) {
			out = append(out, pattern)
		}
	}
	return out
}

func curate(items []item, p config.ModelCatalogPolicy) []item {
	rows := make([]item, 0, len(items))
	for _, row := range items {
		if len(matching(p.Hidden, row)) == 0 {
			rows = append(rows, row)
		}
	}
	if p.Order == "asc" || p.Order == "desc" {
		sort.SliceStable(rows, func(i, j int) bool {
			if p.Order == "desc" {
				return rows[i].id > rows[j].id
			}
			return rows[i].id < rows[j].id
		})
	}
	out := make([]item, 0, len(rows))
	used := make([]bool, len(rows))
	for _, pattern := range p.Pinned {
		for i, row := range rows {
			if !used[i] && len(matching([]string{pattern}, row)) > 0 {
				out = append(out, row)
				used[i] = true
			}
		}
	}
	for i, row := range rows {
		if !used[i] {
			out = append(out, row)
		}
	}
	return out
}

// Transform leaves unsupported catalogs and ineffective policies untouched.
func Transform(body []byte, p config.ModelCatalogPolicy) []byte {
	p, err := p.Normalized()
	if err != nil {
		return body
	}
	if len(p.Hidden) == 0 && len(p.Pinned) == 0 && p.Order == "preserve" {
		return body
	}
	root, key, items, err := parse(body)
	if err != nil {
		return body
	}
	rows := curate(items, p)
	same := len(rows) == len(items)
	if same {
		for i, row := range rows {
			if row.index != i {
				same = false
				break
			}
		}
	}
	if same {
		return body
	}
	out := make([]json.RawMessage, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.raw)
	}
	root[key], err = compact(out)
	if err != nil {
		return body
	}
	var hasMore bool
	knownClaude := json.Unmarshal(root["has_more"], &hasMore) == nil && string(root["has_more"]) != "null"
	if key == "data" && knownClaude {
		for _, field := range []string{"first_id", "last_id"} {
			if _, exists := root[field]; !exists {
				continue
			}
			id := ""
			if len(rows) > 0 {
				if field == "first_id" {
					id = rows[0].id
				} else {
					id = rows[len(rows)-1].id
				}
			}
			root[field], _ = compact(id)
		}
	}
	result, err := compact(root)
	if err != nil {
		return body
	}
	return result
}

func compact(v any) ([]byte, error) {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	if err := e.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}

// Entry describes a catalog item without exposing provider credentials.
type Entry struct {
	ID          string   `json:"id"`
	Label       string   `json:"label"`
	Format      string   `json:"format"`
	Hidden      bool     `json:"hidden"`
	HiddenRules []string `json:"hidden_rules"`
	PinnedRules []string `json:"pinned_rules"`
	Position    int      `json:"position"`
}

// View is the backend-owned preview shared with management clients.
type View struct {
	Format           string                    `json:"format"`
	Policy           config.ModelCatalogPolicy `json:"policy"`
	Entries          []Entry                   `json:"entries"`
	VisibleIDs       []string                  `json:"visible_ids"`
	Counts           Counts                    `json:"counts"`
	ModelSortEnabled bool                      `json:"model_sort_enabled"`
}

type Counts struct {
	Total   int `json:"total"`
	Visible int `json:"visible"`
	Hidden  int `json:"hidden"`
}

// Inspect computes the same curation as Transform while retaining hidden entries.
func Inspect(body []byte, p config.ModelCatalogPolicy, format string) (View, error) {
	p, err := p.Normalized()
	if err != nil {
		return View{}, err
	}
	_, _, items, err := parse(body)
	if err != nil {
		return View{}, err
	}
	rows := curate(items, p)
	result := View{Format: format, Policy: p, Entries: make([]Entry, 0, len(items)), VisibleIDs: make([]string, 0, len(rows)), Counts: Counts{Total: len(items), Visible: len(rows), Hidden: len(items) - len(rows)}}
	positions := make(map[int]int, len(rows))
	for i, row := range rows {
		positions[row.index] = i
		result.VisibleIDs = append(result.VisibleIDs, row.id)
	}
	for _, row := range items {
		hiddenRules, pinnedRules := matching(p.Hidden, row), matching(p.Pinned, row)
		position, visible := positions[row.index]
		if !visible {
			position = -1
		}
		label := row.id
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(row.raw, &fields)
		for _, key := range []string{"display_name", "displayName", "name"} {
			var text string
			if json.Unmarshal(fields[key], &text) == nil && text != "" {
				label = text
				break
			}
		}
		result.Entries = append(result.Entries, Entry{ID: row.id, Label: label, Format: format, Hidden: !visible, HiddenRules: hiddenRules, PinnedRules: pinnedRules, Position: position})
	}
	return result, nil
}
