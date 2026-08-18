package documents

import (
	"context"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const maxNoteEntry = 500
const maxNoteLog = 4000

func ClampNote(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	n := 0
	prevSpace := false
	for _, r := range s {
		if r == '\n' || r == '\r' || r == '\t' {
			r = ' '
		}
		if unicode.IsControl(r) {
			continue
		}
		if r == ' ' {
			if prevSpace || n == 0 {
				continue
			}
			prevSpace = true
		} else {
			prevSpace = false
		}
		if n >= maxNoteEntry {
			break
		}
		b.WriteRune(r)
		n++
	}
	out := strings.TrimSpace(b.String())
	if utf8.RuneCountInString(out) > maxNoteEntry {
		out = string([]rune(out)[:maxNoteEntry])
	}
	return out
}

func mergeNoteLog(chunks ...string) string {
	seen := make(map[string]struct{}, len(chunks))
	parts := make([]string, 0, len(chunks))
	for _, chunk := range chunks {
		for _, line := range strings.Split(chunk, "\n") {
			line = ClampNote(line)
			if line == "" {
				continue
			}
			if _, ok := seen[line]; ok {
				continue
			}
			seen[line] = struct{}{}
			parts = append(parts, line)
		}
	}
	var b strings.Builder
	n := 0
	for i, part := range parts {
		extra := utf8.RuneCountInString(part)
		if i > 0 {
			extra++
		}
		if n+extra > maxNoteLog {
			break
		}
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(part)
		n += extra
	}
	return b.String()
}

func joinSourceNotes(sources []Source) string {
	chunks := make([]string, 0, len(sources))
	for _, src := range sources {
		chunks = append(chunks, src.Note)
	}
	return mergeNoteLog(chunks...)
}

func mergeReviewNote(existing, added string) string {
	return mergeNoteLog(existing, added)
}

func noteForFile(notes []string, i int, fallback string) string {
	if i < len(notes) {
		return ClampNote(notes[i])
	}
	if len(notes) == 0 {
		return ClampNote(fallback)
	}
	return ""
}

func (r *Repo) UploadStats(ctx context.Context, from, to time.Time) (UploadStats, error) {
	db, err := r.db()
	if err != nil {
		return UploadStats{}, err
	}

	docs, err := countByUTCDay(ctx, db, "documents", from, to)
	if err != nil {
		return UploadStats{}, err
	}
	sources, err := countByUTCDay(ctx, db, "sources", from, to)
	if err != nil {
		return UploadStats{}, err
	}

	byDay := make(map[string]UploadDay, len(docs)+len(sources))
	for key, n := range docs {
		byDay[key] = UploadDay{Day: key, Documents: n}
	}
	for key, n := range sources {
		cur := byDay[key]
		cur.Day = key
		cur.Sources = n
		byDay[key] = cur
	}

	keys := make([]string, 0, len(byDay))
	for key := range byDay {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	out := UploadStats{
		Bucket:   "day",
		Timezone: "UTC",
		From:     from.UTC().Format("2006-01-02"),
		To:       to.AddDate(0, 0, -1).UTC().Format("2006-01-02"),
		Days:     make([]UploadDay, 0, len(keys)),
	}
	if !to.After(from) {
		out.To = out.From
	}
	for _, key := range keys {
		day := byDay[key]
		out.Days = append(out.Days, day)
		out.Total.Documents += day.Documents
		out.Total.Sources += day.Sources
	}
	return out, nil
}

func countByUTCDay(ctx context.Context, db querier, table string, from, to time.Time) (map[string]int, error) {
	if table != "documents" && table != "sources" {
		return nil, ErrInvalid
	}
	rows, err := db.Query(ctx, `
		SELECT (created_at AT TIME ZONE 'UTC')::date AS day, COUNT(*)::int
		FROM `+table+`
		WHERE created_at >= $1 AND created_at < $2
		GROUP BY 1
		ORDER BY 1`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]int, 32)
	for rows.Next() {
		var day time.Time
		var n int
		if err := rows.Scan(&day, &n); err != nil {
			return nil, err
		}
		out[day.UTC().Format("2006-01-02")] = n
	}
	return out, rows.Err()
}
