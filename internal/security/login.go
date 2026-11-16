package security

import (
	"context"
	"fmt"

	"github.com/leeska/Lodestar/internal/model"
)

func (m *Module) ObserveLogin(ctx context.Context, actor, source string, success, knownDevice bool) error {
	if success && knownDevice {
		return nil
	}
	m.login.Lock()
	defer m.login.Unlock()
	config, err := m.Settings(ctx)
	if err != nil || !config.LoginAlerts {
		return err
	}
	kind, title := "login.failed", "后台登录失败"
	if success {
		kind, title = "login.new_device", "后台新设备登录"
	}
	bucket := fmt.Sprintf("%s:%s:%d", kind, model.Hash(source), m.now().Unix()/300)
	result, err := m.DB.ExecContext(ctx, `INSERT INTO security_login_notices(bucket,at) VALUES(?,?) ON CONFLICT(bucket) DO NOTHING`, bucket, m.now().Unix())
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed == 0 {
		return nil
	}
	if _, err := m.DB.ExecContext(ctx, `DELETE FROM security_login_notices WHERE at<?`, m.now().AddDate(0, 0, -7).Unix()); err != nil {
		return err
	}
	if m.Events == nil {
		return nil
	}
	if success {
		if err := m.Events.Audit(ctx, model.AuditEntry{Actor: actor, Source: source, Action: kind, Status: 200, Detail: "没有有效的既有设备凭据"}); err != nil {
			return err
		}
	}
	return m.Events.RecordEvent(ctx, model.Event{NodeID: "security:login", NodeName: title, Kind: model.EventSecurity, Active: true, Message: title + "：" + actor + "\n来源：" + source + "\n来源链为记录值；新设备指缺少有效的已识别设备 Cookie", At: m.now().Unix()})
}
