package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

func prefix(id string) string { return "assistant:conversation:" + id + ":" }

func (m *Module) seal(ctx context.Context, id, value string) (string, error) {
	if err := m.Vault.Put(ctx, id, value); err != nil {
		return "", err
	}
	raw, err := json.Marshal(map[string]string{"encrypted": id})
	return string(raw), err
}

func (m *Module) open(ctx context.Context, raw, expected string) (string, error) {
	var reference struct {
		Encrypted string `json:"encrypted"`
	}
	if json.Unmarshal([]byte(raw), &reference) != nil || reference.Encrypted == "" {
		return raw, nil
	}
	if !strings.HasPrefix(reference.Encrypted, expected) {
		return "", fmt.Errorf("conversation ciphertext reference mismatch")
	}
	return m.Vault.Get(ctx, reference.Encrypted)
}

func (m *Module) ProtectHistory(ctx context.Context) error {
	type record struct {
		id, title, state string
	}
	rows, err := m.DB.QueryContext(ctx, `SELECT id,title,state FROM assistant_conversations`)
	if err != nil {
		return err
	}
	conversations := []record{}
	for rows.Next() {
		var item record
		if err := rows.Scan(&item.id, &item.title, &item.state); err != nil {
			rows.Close()
			return err
		}
		conversations = append(conversations, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, item := range conversations {
		for _, field := range []struct{ name, value string }{{"title", item.title}, {"state", item.state}} {
			var reference map[string]any
			if json.Unmarshal([]byte(field.value), &reference) == nil && reference["encrypted"] != nil {
				if _, err := m.open(ctx, field.value, prefix(item.id)+field.name); err != nil {
					return err
				}
				continue
			}
			sealed, err := m.seal(ctx, prefix(item.id)+field.name, field.value)
			if err != nil {
				return err
			}
			if _, err := m.DB.ExecContext(ctx, `UPDATE assistant_conversations SET `+field.name+`=? WHERE id=?`, sealed, item.id); err != nil {
				return err
			}
		}
	}
	var after int64
	for {
		rows, err := m.DB.QueryContext(ctx, `SELECT id,conversation_id,content FROM assistant_messages WHERE id>? ORDER BY id LIMIT 100`, after)
		if err != nil {
			return err
		}
		type message struct {
			id      int64
			owner   string
			content string
		}
		batch := []message{}
		for rows.Next() {
			var item message
			if err := rows.Scan(&item.id, &item.owner, &item.content); err != nil {
				rows.Close()
				return err
			}
			batch = append(batch, item)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(batch) == 0 {
			return nil
		}
		for _, item := range batch {
			after = item.id
			var reference map[string]any
			if json.Unmarshal([]byte(item.content), &reference) == nil && reference["encrypted"] != nil {
				continue
			}
			sealed, err := m.seal(ctx, fmt.Sprintf("%smessage:%d", prefix(item.owner), item.id), item.content)
			if err != nil {
				return err
			}
			if _, err := m.DB.ExecContext(ctx, `UPDATE assistant_messages SET content=? WHERE id=?`, sealed, item.id); err != nil {
				return err
			}
		}
	}
}
