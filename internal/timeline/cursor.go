package timeline

import (
	"context"
	"encoding/json"
	"fmt"
)

type Cursor struct {
	Event  int64 `json:"event"`
	Audit  int64 `json:"audit"`
	Module int64 `json:"module"`
}

func (m *Module) Watermark(ctx context.Context) (Cursor, error) {
	var cursor Cursor
	err := m.DB.QueryRowContext(ctx, `SELECT coalesce((SELECT max(id) FROM events),0),coalesce((SELECT max(id) FROM audit_log),0),coalesce((SELECT max(id) FROM timeline),0)`).Scan(&cursor.Event, &cursor.Audit, &cursor.Module)
	return cursor, err
}

func (m *Module) After(ctx context.Context, cursor Cursor) ([]Entry, Cursor, error) {
	out := []Entry{}
	queries := []struct {
		sql   string
		after *int64
	}{
		{`SELECT id,at,'event',kind,CASE WHEN active THEN 'bad' ELSE 'good' END,node_name,message,node_id,'',origin FROM events WHERE id>? ORDER BY id LIMIT 100`, &cursor.Event},
		{`SELECT id,at,'audit',action,CASE WHEN status>=400 THEN 'warn' ELSE 'info' END,actor,detail,node_id,'',origin FROM audit_log WHERE id>? ORDER BY id LIMIT 100`, &cursor.Audit},
		{`SELECT id,at,source,kind,level,title,body,ref,data,origin FROM timeline WHERE id>? ORDER BY id LIMIT 100`, &cursor.Module},
	}
	for _, query := range queries {
		rows, err := m.DB.QueryContext(ctx, query.sql, *query.after)
		if err != nil {
			return nil, cursor, err
		}
		for rows.Next() {
			var entry Entry
			var id int64
			var data string
			if err := rows.Scan(&id, &entry.At, &entry.Source, &entry.Kind, &entry.Level, &entry.Title, &entry.Body, &entry.Ref, &data, &entry.Origin); err != nil {
				rows.Close()
				return nil, cursor, err
			}
			*query.after = id
			entry.ID = fmt.Sprintf("%s:%d", entry.Source, id)
			if data != "" {
				entry.Data = json.RawMessage(data)
			}
			out = append(out, entry)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, cursor, err
		}
	}
	return out, cursor, nil
}
