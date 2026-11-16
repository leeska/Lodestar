package security

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/leeska/Lodestar/internal/action"
	"github.com/leeska/Lodestar/internal/assets"
	"github.com/leeska/Lodestar/internal/model"
)

func formatCount(count int) string { return strconv.Itoa(count) }
func passwordCount(ctx context.Context, client *http.Client, password string) (int64, error) {
	digest := sha1.Sum([]byte(password))
	hash := strings.ToUpper(hex.EncodeToString(digest[:]))
	request, err := http.NewRequestWithContext(ctx, "GET", "https://api.pwnedpasswords.com/range/"+hash[:5], nil)
	if err != nil {
		return 0, errors.New("无法创建 HIBP 查询")
	}
	request.Header.Set("Add-Padding", "true")
	request.Header.Set("User-Agent", "Lodestar/3.9")
	request.Header.Set("Accept", "text/plain")
	response, err := client.Do(request)
	if err != nil {
		return 0, errors.New("HIBP 查询失败")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return 0, errors.New("HIBP 暂不可用")
	}
	content, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(content) > 1<<20 {
		return 0, errors.New("HIBP 响应超出限制")
	}
	scanner := bufio.NewScanner(strings.NewReader(string(content)))
	valid := 0
	var count int64
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		suffix, number, ok := strings.Cut(line, ":")
		if !ok || len(suffix) != 35 {
			return 0, errors.New("HIBP 响应格式不正确")
		}
		if _, err := hex.DecodeString(suffix + "0"); err != nil {
			return 0, errors.New("HIBP 响应格式不正确")
		}
		seen, err := strconv.ParseInt(number, 10, 64)
		if err != nil || seen < 0 {
			return 0, errors.New("HIBP 响应计数不正确")
		}
		valid++
		if strings.EqualFold(suffix, hash[5:]) {
			count = seen
		}
	}
	if scanner.Err() != nil || valid == 0 {
		return 0, errors.New("HIBP 响应不完整")
	}
	return count, nil
}
func (m *Module) passwordClient() *http.Client {
	if m.PasswordClient != nil {
		client := *m.PasswordClient
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		return &client
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &http.Client{Timeout: 10 * time.Second, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func (m *Module) passwordVersion(ctx context.Context, id string) ([]byte, error) {
	var version []byte
	err := m.DB.QueryRowContext(ctx, `SELECT nonce FROM vault WHERE id=?`, "asset:"+id+":password").Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return version, err
}
func (m *Module) CheckPasswords(ctx context.Context, force bool) (int, error) {
	if !m.check.TryLock() {
		if !force {
			return 0, nil
		}
		return 0, &action.Error{Status: 409, Message: "安全检查正在进行"}
	}
	defer m.check.Unlock()
	config, err := m.Settings(ctx)
	if err != nil {
		return 0, err
	}
	if !config.PasswordChecks {
		if force {
			return 0, action.Invalid("请先明确启用 HIBP 检查")
		}
		return 0, nil
	}
	if force && !allows(ctx, "assets", action.Danger) {
		return 0, &action.Error{Status: 403, Message: "需要资产敏感字段权限"}
	}
	items, err := m.Assets.List(ctx, assets.Filter{Kind: "account"}, m.now())
	if err != nil {
		return 0, err
	}
	client := m.passwordClient()
	if m.PasswordClient == nil {
		defer client.CloseIdleConnections()
	}
	checked := 0
	for _, asset := range items {
		if checked >= 100 || ctx.Err() != nil {
			break
		}
		profile, err := m.profile(ctx, asset.ID)
		if err != nil {
			return checked, err
		}
		if !profile.MonitorPassword {
			continue
		}
		version, err := m.passwordVersion(ctx, asset.ID)
		if err != nil {
			return checked, err
		}
		if !force && (bytes.Equal(version, profile.passwordVersion) && profile.CheckedAt > m.now().Add(-7*24*time.Hour).Unix() || profile.CheckError && profile.LastAttempt > m.now().Add(-24*time.Hour).Unix()) {
			continue
		}
		if !asset.Secrets["password"] {
			if _, err := m.DB.ExecContext(ctx, `UPDATE security_profiles SET breach_count=-1,checked_at=0,check_error=0,last_attempt=? WHERE asset_id=?`, m.now().Unix(), asset.ID); err != nil {
				return checked, err
			}
			continue
		}
		password, err := m.Assets.Secret(ctx, asset.ID, "password")
		if err != nil {
			return checked, err
		}
		query, cancel := context.WithTimeout(ctx, 10*time.Second)
		count, queryErr := passwordCount(query, client, password)
		cancel()
		password = ""
		checked++
		if queryErr != nil {
			if _, err := m.DB.ExecContext(context.WithoutCancel(ctx), `UPDATE security_profiles SET check_error=1,last_attempt=? WHERE asset_id=?`, m.now().Unix(), asset.ID); err != nil {
				return checked, err
			}
			continue
		}
		result, err := m.DB.ExecContext(ctx, `UPDATE security_profiles SET breach_count=?,checked_at=?,last_attempt=?,check_error=0,password_version=? WHERE asset_id=? AND EXISTS(SELECT 1 FROM vault WHERE id=? AND nonce=?)`, count, m.now().Unix(), m.now().Unix(), version, asset.ID, "asset:"+asset.ID+":password", version)
		if err != nil {
			return checked, err
		}
		changed, _ := result.RowsAffected()
		if changed != 1 {
			continue
		}
		if count > 0 && profile.BreachCount <= 0 && m.Events != nil {
			if err := m.Events.RecordEvent(ctx, model.Event{NodeID: "asset:" + asset.ID, NodeName: asset.Name, Kind: model.EventSecurity, Active: true, Message: fmt.Sprintf("密码出现在已知泄露库中（%d 次），请更换密码；本次没有发送密码原文或完整哈希", count), At: m.now().Unix()}); err != nil {
				return checked, err
			}
		} else if count == 0 && profile.BreachCount > 0 && m.Events != nil {
			if err := m.Events.RecordEvent(ctx, model.Event{NodeID: "asset:" + asset.ID, NodeName: asset.Name, Kind: model.EventSecurity, Active: false, Message: "本次密码未命中已知泄露库；不代表密码安全", At: m.now().Unix()}); err != nil {
				return checked, err
			}
		}
	}
	return checked, nil
}
