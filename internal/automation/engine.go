package automation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/leeska/Lodestar/internal/action"
	"github.com/leeska/Lodestar/internal/model"
	"github.com/leeska/Lodestar/internal/timeline"
	"github.com/leeska/Lodestar/internal/vault"
)

type Run struct {
	ID         string `json:"id"`
	RuleID     string `json:"rule_id"`
	RuleName   string `json:"rule_name"`
	TriggerKey string `json:"trigger_key"`
	Action     string `json:"action"`
	Status     string `json:"status"`
	At         int64  `json:"at"`
	FinishedAt int64  `json:"finished_at"`
	actor      action.Actor
}

func (m *Module) liveActor(ctx context.Context, actor action.Actor) (action.Actor, error) {
	if m.resolve == nil {
		if actor.KeyID != "" {
			return actor, &action.Error{Status: 403, Message: "无法重新核验访问密钥"}
		}
		return actor, nil
	}
	return m.resolve(ctx, actor)
}
func (m *Module) Tick(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	rules, err := m.List(ctx)
	if err != nil {
		return err
	}
	for _, rule := range rules {
		if ctx.Err() != nil {
			break
		}
		if !rule.Enabled {
			continue
		}
		release, err := m.acquire(rule.ID)
		if err != nil {
			continue
		}
		rule, err = m.rule(ctx, rule.ID)
		if err != nil {
			release()
			continue
		}
		if !rule.Enabled {
			release()
			continue
		}
		switch rule.Trigger {
		case "cron":
			spec, err := schedule(rule)
			now := m.now().Truncate(time.Minute)
			if err == nil && spec.Next(now.Add(-time.Minute)).Equal(now) {
				_ = m.attempt(ctx, rule, fmt.Sprintf("cron:%d", now.Unix()), timeline.Entry{Source: "cron", Kind: "cron", Level: "info", Title: rule.Name, At: now.Unix()})
			}
		case "timeline":
			entries, cursor, err := m.Timeline.After(ctx, rule.cursor)
			if err == nil {
				for _, entry := range entries {
					if ctx.Err() != nil {
						break
					}
					if matches(rule, entry) {
						_ = m.attempt(ctx, rule, entry.ID, entry)
					}
				}
				if ctx.Err() == nil {
					raw, _ := json.Marshal(cursor)
					_, err = m.DB.ExecContext(ctx, `UPDATE automation_rules SET cursor=? WHERE id=?`, string(raw), rule.ID)
				}
			}
			if err != nil {
				release()
				return err
			}
		}
		release()
	}
	return nil
}
func (m *Module) report(ctx context.Context, run Run, status string) error {
	ctx = action.WithOrigin(context.WithoutCancel(ctx), "automation")
	code, level := 200, timeline.Good
	if status == "failed" {
		code, level = 500, timeline.Bad
	}
	if status == "pending" {
		code, level = 202, timeline.Warn
	}
	body := run.Action + " · " + status
	if err := m.Timeline.Emit(ctx, timeline.Entry{Source: "automation", Kind: status, Level: level, Title: "自动化 · " + run.RuleName, Body: body, Ref: "automation:" + run.ID}); err != nil {
		return err
	}
	if m.Events != nil {
		return m.Events.Audit(ctx, model.AuditEntry{Actor: "automation:" + run.RuleName, Source: "automation", Action: run.Action, Status: code, Detail: status})
	}
	return nil
}
func (m *Module) finish(ctx context.Context, run Run, output any, executionErr error) error {
	status := "succeeded"
	detail := map[string]any{"result": output}
	if executionErr != nil {
		status = "failed"
		detail = map[string]any{"error": executionErr.Error()}
	}
	raw, err := json.Marshal(detail)
	if err != nil {
		status = "failed"
		raw = []byte(`{"error":"执行结果无法编码"}`)
	}
	if len(raw) > 256<<10 {
		raw = []byte(`{"error":"执行结果超过 256 KB，未保存"}`)
	}
	durable := context.WithoutCancel(ctx)
	if err := m.Vault.Put(durable, "automation:run:"+run.ID+":result", string(raw)); err != nil {
		return err
	}
	if _, err := m.DB.ExecContext(durable, `UPDATE automation_runs SET status=?,finished_at=? WHERE id=? AND status IN ('running','pending')`, status, m.now().Unix(), run.ID); err != nil {
		return err
	}
	return m.report(durable, run, status)
}
func (m *Module) attempt(ctx context.Context, rule Rule, key string, entry timeline.Entry) error {
	if !matches(rule, entry) {
		return nil
	}
	var last int64
	if err := m.DB.QueryRowContext(ctx, `SELECT last_attempt FROM automation_rules WHERE id=?`, rule.ID).Scan(&last); err != nil {
		return err
	}
	if last > 0 && m.now().Unix()-last < int64(rule.Cooldown) {
		return nil
	}
	run := Run{ID: model.Secret()[:24], RuleID: rule.ID, RuleName: rule.Name, TriggerKey: key, Action: rule.Action, Status: "running", At: m.now().Unix(), actor: rule.actor}
	policy, _ := json.Marshal(rule.actor)
	claim, err := m.DB.ExecContext(ctx, `INSERT INTO automation_runs(id,rule_id,rule_name,trigger_key,action,actor,status,at) VALUES(?,?,?,?,?,?,'running',?) ON CONFLICT(rule_id,trigger_key) DO NOTHING`, run.ID, rule.ID, rule.Name, key, rule.Action, string(policy), run.At)
	if err != nil {
		return err
	}
	count, _ := claim.RowsAffected()
	if count == 0 {
		return nil
	}
	if err := m.report(ctx, run, "started"); err != nil {
		return err
	}
	if _, err := m.DB.ExecContext(ctx, `UPDATE automation_rules SET last_attempt=? WHERE id=?`, run.At, rule.ID); err != nil {
		return m.finish(ctx, run, nil, err)
	}
	actor, err := m.liveActor(ctx, rule.actor)
	if err != nil {
		return m.finish(ctx, run, nil, err)
	}
	if rule.Trigger == "timeline" && !actor.Allows(action.Action{Name: "timeline.list", Risk: action.Read}) {
		return m.finish(ctx, run, nil, action.Invalid("创建者已失去时间线读取权限"))
	}
	target, err := m.target(actor, rule.Action)
	if err != nil {
		return m.finish(ctx, run, nil, err)
	}
	raw, err := m.Vault.Get(ctx, rule.argsKey)
	if err != nil {
		return m.finish(ctx, run, nil, err)
	}
	args, err := renderArgs(raw, entry)
	if err != nil {
		return m.finish(ctx, run, nil, err)
	}
	if err := m.Vault.Put(ctx, "automation:run:"+run.ID+":args", string(args)); err != nil {
		return m.finish(ctx, run, nil, err)
	}
	if target.NeedsConfirm() {
		var pending int
		if err := m.DB.QueryRowContext(ctx, `SELECT count(*) FROM automation_runs WHERE status='pending'`).Scan(&pending); err != nil {
			return m.finish(ctx, run, nil, err)
		}
		if pending >= 100 {
			return m.finish(ctx, run, nil, action.Invalid("待确认队列已满（100 项）"))
		}
		result, err := m.DB.ExecContext(ctx, `UPDATE automation_runs SET status='pending' WHERE id=? AND status='running' AND (SELECT count(*) FROM automation_runs WHERE status='pending')<100`, run.ID)
		if err != nil {
			return err
		}
		changed, _ := result.RowsAffected()
		if changed != 1 {
			return m.finish(ctx, run, nil, action.Invalid("待确认队列已满"))
		}
		if err := m.report(ctx, run, "pending"); err != nil {
			return err
		}
		if m.Events != nil {
			return m.Events.RecordEvent(action.WithOrigin(ctx, "automation"), model.Event{NodeID: "automation:" + run.ID, NodeName: rule.Name, Kind: model.EventAutomation, Active: true, Message: "危险动作等待后台确认：" + target.Title, At: m.now().Unix()})
		}
		return nil
	}
	actor.Name = "automation:" + rule.Name
	execution, cancel := context.WithTimeout(action.WithOrigin(action.WithActor(ctx, actor), "automation"), 20*time.Second)
	defer cancel()
	output, executionErr := m.execute(execution, target.Name, args, false)
	return m.finish(ctx, run, output, executionErr)
}

func (m *Module) Recover(ctx context.Context) error {
	_, err := m.DB.ExecContext(ctx, `UPDATE automation_runs SET status='interrupted',finished_at=? WHERE status='running'`, m.now().Unix())
	return err
}
func (m *Module) Runs(ctx context.Context) ([]Run, error) {
	rows, err := m.DB.QueryContext(ctx, `SELECT id,rule_id,rule_name,trigger_key,action,status,at,finished_at FROM automation_runs ORDER BY at DESC,rowid DESC LIMIT 200`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	runs := []Run{}
	for rows.Next() {
		var run Run
		if err := rows.Scan(&run.ID, &run.RuleID, &run.RuleName, &run.TriggerKey, &run.Action, &run.Status, &run.At, &run.FinishedAt); err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}
func (m *Module) protectedRun(ctx context.Context, id string) (Run, error) {
	var run Run
	var policy string
	err := m.DB.QueryRowContext(ctx, `SELECT id,rule_id,rule_name,trigger_key,action,status,at,finished_at,actor FROM automation_runs WHERE id=?`, id).Scan(&run.ID, &run.RuleID, &run.RuleName, &run.TriggerKey, &run.Action, &run.Status, &run.At, &run.FinishedAt, &policy)
	if errors.Is(err, sql.ErrNoRows) {
		return run, &action.Error{Status: 404, Message: "没有这次执行"}
	}
	if err != nil {
		return run, err
	}
	if err := json.Unmarshal([]byte(policy), &run.actor); err != nil {
		return run, err
	}
	caller, ok := action.ActorFrom(ctx)
	if !ok {
		return run, &action.Error{Status: 401, Message: "需要已认证的调用者"}
	}
	if _, err := m.target(caller, run.Action); err != nil {
		return run, err
	}
	return run, nil
}
func (m *Module) Details(ctx context.Context, id string) (any, error) {
	run, err := m.protectedRun(ctx, id)
	if err != nil {
		return nil, err
	}
	args, err := m.Vault.Get(ctx, "automation:run:"+id+":args")
	if err != nil && !errors.Is(err, vault.ErrNotFound) {
		return nil, err
	}
	result, err := m.Vault.Get(ctx, "automation:run:"+id+":result")
	if err != nil && !errors.Is(err, vault.ErrNotFound) {
		return nil, err
	}
	return map[string]any{"run": run, "args": json.RawMessage(orNull(args)), "result": json.RawMessage(orNull(result))}, nil
}
func orNull(value string) string {
	if value == "" {
		return "null"
	}
	return value
}
func (m *Module) Confirm(ctx context.Context, id string, approve bool) error {
	run, err := m.protectedRun(ctx, id)
	if err != nil {
		return err
	}
	release, err := m.acquire(run.RuleID)
	if err != nil {
		return err
	}
	defer release()
	if run.Status != "pending" {
		return &action.Error{Status: 409, Message: "这项确认已被处理"}
	}
	if approve {
		actor, err := m.liveActor(ctx, run.actor)
		if err != nil {
			return err
		}
		if _, err := m.target(actor, run.Action); err != nil {
			return err
		}
	}
	status := "cancelled"
	if approve {
		status = "running"
	}
	result, err := m.DB.ExecContext(ctx, `UPDATE automation_runs SET status=?,finished_at=? WHERE id=? AND status='pending'`, status, m.now().Unix(), id)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count != 1 {
		return &action.Error{Status: 409, Message: "这项确认已被处理"}
	}
	if !approve {
		return m.report(ctx, run, "cancelled")
	}
	args, err := m.Vault.Get(ctx, "automation:run:"+id+":args")
	if err != nil {
		return m.finish(ctx, run, nil, err)
	}
	actor, err := m.liveActor(ctx, run.actor)
	if err != nil {
		return m.finish(ctx, run, nil, err)
	}
	actor.Name = "automation:" + run.RuleName
	execution, cancel := context.WithTimeout(action.WithOrigin(action.WithActor(ctx, actor), "automation"), 20*time.Second)
	defer cancel()
	output, executionErr := m.execute(execution, run.Action, json.RawMessage(args), true)
	return m.finish(ctx, run, output, executionErr)
}
