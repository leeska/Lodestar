package finance

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/leeska/Lodestar/internal/action"
	"github.com/leeska/Lodestar/internal/assets"
	"github.com/leeska/Lodestar/internal/model"
	"github.com/leeska/Lodestar/internal/module"
	"github.com/leeska/Lodestar/internal/storage"
)

type Settings struct {
	Currency string `json:"currency"`
	Timezone string `json:"timezone"`
}
type Account struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Kind           string `json:"kind"`
	Currency       string `json:"currency"`
	OpeningBalance string `json:"opening_balance"`
	BalanceMode    string `json:"balance_mode"`
	ManualBalance  string `json:"manual_balance"`
	Balance        string `json:"balance"`
}
type Transaction struct {
	ID          int64  `json:"id"`
	AccountID   string `json:"account_id"`
	AccountName string `json:"account_name"`
	At          int64  `json:"at"`
	Amount      string `json:"amount"`
	Currency    string `json:"currency"`
	Category    string `json:"category"`
	Note        string `json:"note"`
	Ref         string `json:"ref"`
}
type Budget struct {
	ID       int64  `json:"id"`
	Category string `json:"category"`
	Period   string `json:"period"`
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
	Spent    string `json:"spent,omitempty"`
}
type Snapshot struct {
	Day      string          `json:"day"`
	At       int64           `json:"at"`
	Currency string          `json:"currency"`
	NetWorth string          `json:"net_worth"`
	Balances json.RawMessage `json:"balances"`
}
type Module struct {
	DB     *sql.DB
	Assets *assets.Store
	Nodes  interface {
		Nodes(context.Context, time.Time) ([]model.NodeView, error)
	}
	Now func() time.Time
	mu  sync.Mutex
}

func (*Module) ID() string { return "finance" }
func (m *Module) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}
func (*Module) Migrations() []storage.Migration {
	return []storage.Migration{{Module: "finance", Version: 1, Name: "accounts transactions budgets and daily snapshots", Up: func(c *storage.MigrationContext) error {
		_, err := c.Tx.Exec(`CREATE TABLE finance_settings(id INTEGER PRIMARY KEY CHECK(id=1),config TEXT NOT NULL);
CREATE TABLE finance_accounts(id TEXT PRIMARY KEY,name TEXT NOT NULL,kind TEXT NOT NULL,currency TEXT NOT NULL,
opening_minor INTEGER NOT NULL,balance_mode TEXT NOT NULL,manual_minor INTEGER NOT NULL);
CREATE TABLE finance_transactions(id INTEGER PRIMARY KEY AUTOINCREMENT,account_id TEXT NOT NULL REFERENCES finance_accounts(id) ON DELETE RESTRICT,
at INTEGER NOT NULL,amount_minor INTEGER NOT NULL,currency TEXT NOT NULL,category TEXT NOT NULL,note TEXT NOT NULL DEFAULT '',ref TEXT NOT NULL DEFAULT '',import_key TEXT);
CREATE INDEX finance_transactions_account ON finance_transactions(account_id,at);
CREATE INDEX finance_transactions_at ON finance_transactions(at);
CREATE UNIQUE INDEX finance_import_key ON finance_transactions(account_id,import_key) WHERE import_key IS NOT NULL;
CREATE TABLE finance_budgets(id INTEGER PRIMARY KEY AUTOINCREMENT,category TEXT NOT NULL,period TEXT NOT NULL,amount_minor INTEGER NOT NULL,currency TEXT NOT NULL,UNIQUE(category,period,currency));
CREATE TABLE finance_snapshots(day TEXT NOT NULL,at INTEGER NOT NULL,currency TEXT NOT NULL,net_minor INTEGER NOT NULL,balances TEXT NOT NULL,PRIMARY KEY(day,currency));`)
		return err
	}}}
}
func currency(value string) (string, error) {
	value = strings.ToUpper(strings.TrimSpace(value))
	if value == "" {
		value = "CNY"
	}
	if !slices.Contains(model.PriceUnits, value) {
		return "", action.Invalid("不支持这个币种")
	}
	return value, nil
}
func (m *Module) Settings(ctx context.Context) (Settings, error) {
	config := Settings{Currency: "CNY", Timezone: "UTC"}
	var raw string
	err := m.DB.QueryRowContext(ctx, `SELECT config FROM finance_settings WHERE id=1`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return config, nil
	}
	if err != nil {
		return config, err
	}
	err = json.Unmarshal([]byte(raw), &config)
	return config, err
}
func (m *Module) SaveSettings(ctx context.Context, config Settings) error {
	var err error
	config.Currency, err = currency(config.Currency)
	if err != nil {
		return err
	}
	if config.Timezone == "" {
		return action.Invalid("请选择时区")
	}
	if _, err := time.LoadLocation(config.Timezone); err != nil {
		return action.Invalid("时区无效")
	}
	raw, _ := json.Marshal(config)
	m.mu.Lock()
	defer m.mu.Unlock()
	_, err = m.DB.ExecContext(ctx, `INSERT INTO finance_settings(id,config) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET config=excluded.config`, string(raw))
	return err
}
func (m *Module) Accounts(ctx context.Context) ([]Account, error) {
	rows, err := m.DB.QueryContext(ctx, `SELECT a.id,a.name,a.kind,a.currency,a.opening_minor,a.balance_mode,a.manual_minor,
CASE WHEN a.balance_mode='manual' THEN a.manual_minor ELSE a.opening_minor+COALESCE((SELECT sum(t.amount_minor) FROM finance_transactions t WHERE t.account_id=a.id),0) END
FROM finance_accounts a ORDER BY a.name,a.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []Account{}
	for rows.Next() {
		var account Account
		var opening, manual, balance int64
		if err := rows.Scan(&account.ID, &account.Name, &account.Kind, &account.Currency, &opening, &account.BalanceMode, &manual, &balance); err != nil {
			return nil, err
		}
		account.OpeningBalance, account.ManualBalance, account.Balance = FormatMoney(opening), FormatMoney(manual), FormatMoney(balance)
		list = append(list, account)
	}
	return list, rows.Err()
}
func (m *Module) account(ctx context.Context, id string) (Account, error) {
	list, err := m.Accounts(ctx)
	if err != nil {
		return Account{}, err
	}
	for _, account := range list {
		if account.ID == id {
			return account, nil
		}
	}
	return Account{}, &action.Error{Status: 404, Message: "账户不存在"}
}
func (m *Module) SaveAccount(ctx context.Context, account Account) (Account, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	account.Name = strings.TrimSpace(account.Name)
	if account.Name == "" || utf8.RuneCountInString(account.Name) > 80 {
		return account, action.Invalid("账户名称须为 1–80 个字")
	}
	if !slices.Contains([]string{"cash", "bank", "investment", "liability", "other"}, account.Kind) {
		return account, action.Invalid("账户类型无效")
	}
	var err error
	account.Currency, err = currency(account.Currency)
	if err != nil {
		return account, err
	}
	if account.BalanceMode == "" {
		account.BalanceMode = "ledger"
	}
	if account.BalanceMode != "ledger" && account.BalanceMode != "manual" {
		return account, action.Invalid("余额模式须为 ledger 或 manual")
	}
	if account.OpeningBalance == "" {
		account.OpeningBalance = "0"
	}
	if account.ManualBalance == "" {
		account.ManualBalance = "0"
	}
	opening, err := ParseMoney(account.OpeningBalance)
	if err != nil {
		return account, err
	}
	manual, err := ParseMoney(account.ManualBalance)
	if err != nil {
		return account, err
	}
	if account.ID == "" {
		account.ID = model.Secret()[:16]
		result, err := m.DB.ExecContext(ctx, `INSERT INTO finance_accounts(id,name,kind,currency,opening_minor,balance_mode,manual_minor) SELECT ?,?,?,?,?,?,? WHERE (SELECT count(*) FROM finance_accounts)<100`, account.ID, account.Name, account.Kind, account.Currency, opening, account.BalanceMode, manual)
		if err != nil {
			return account, err
		}
		count, _ := result.RowsAffected()
		if count == 0 {
			return account, action.Invalid("最多保存 100 个账户")
		}
	} else {
		old, err := m.account(ctx, account.ID)
		if err != nil {
			return account, err
		}
		if old.Currency != account.Currency {
			var count int
			if err := m.DB.QueryRowContext(ctx, `SELECT count(*) FROM finance_transactions WHERE account_id=?`, account.ID).Scan(&count); err != nil {
				return account, err
			}
			if count > 0 {
				return account, action.Invalid("有流水的账户不能更换币种，请新建账户")
			}
		}
		var sum int64
		if err := m.DB.QueryRowContext(ctx, `SELECT COALESCE(sum(amount_minor),0) FROM finance_transactions WHERE account_id=?`, account.ID).Scan(&sum); err != nil {
			return account, err
		}
		if _, err := addMoney(opening, sum); err != nil {
			return account, err
		}
		if _, err := m.DB.ExecContext(ctx, `UPDATE finance_accounts SET name=?,kind=?,currency=?,opening_minor=?,balance_mode=?,manual_minor=? WHERE id=?`, account.Name, account.Kind, account.Currency, opening, account.BalanceMode, manual, account.ID); err != nil {
			return account, err
		}
	}
	return m.account(ctx, account.ID)
}
func (m *Module) DeleteAccount(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.account(ctx, id); err != nil {
		return err
	}
	var count int
	if err := m.DB.QueryRowContext(ctx, `SELECT count(*) FROM finance_transactions WHERE account_id=?`, id).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return action.Invalid("账户还有流水，请先明确处理流水再删除账户")
	}
	_, err := m.DB.ExecContext(ctx, `DELETE FROM finance_accounts WHERE id=?`, id)
	return err
}

type Query struct {
	AccountID, Category, Month string
	Before                     int64
	Limit                      int
}

func (m *Module) month(ctx context.Context, period string) (time.Time, time.Time, error) {
	config, err := m.Settings(ctx)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	location, err := time.LoadLocation(config.Timezone)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	if period == "" {
		period = m.now().In(location).Format("2006-01")
	}
	start, err := time.ParseInLocation("2006-01", period, location)
	if err != nil || len(period) != 7 {
		return start, start, action.Invalid("月份须为 YYYY-MM")
	}
	return start, start.AddDate(0, 1, 0), nil
}
func (m *Module) Transactions(ctx context.Context, query Query) ([]Transaction, error) {
	where, args := "1=1", []any{}
	if query.AccountID != "" {
		where += " AND t.account_id=?"
		args = append(args, query.AccountID)
	}
	if query.Category != "" {
		where += " AND t.category=?"
		args = append(args, query.Category)
	}
	if query.Month != "" {
		start, end, err := m.month(ctx, query.Month)
		if err != nil {
			return nil, err
		}
		where += " AND t.at>=? AND t.at<?"
		args = append(args, start.Unix(), end.Unix())
	}
	if query.Before > 0 {
		where += " AND t.id<?"
		args = append(args, query.Before)
	}
	limit := query.Limit
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	args = append(args, limit)
	rows, err := m.DB.QueryContext(ctx, `SELECT t.id,t.account_id,a.name,t.at,t.amount_minor,t.currency,t.category,t.note,t.ref FROM finance_transactions t JOIN finance_accounts a ON a.id=t.account_id WHERE `+where+` ORDER BY t.id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []Transaction{}
	for rows.Next() {
		var transaction Transaction
		var amount int64
		if err := rows.Scan(&transaction.ID, &transaction.AccountID, &transaction.AccountName, &transaction.At, &amount, &transaction.Currency, &transaction.Category, &transaction.Note, &transaction.Ref); err != nil {
			return nil, err
		}
		transaction.Amount = FormatMoney(amount)
		list = append(list, transaction)
	}
	return list, rows.Err()
}
func (m *Module) validateTransaction(ctx context.Context, transaction *Transaction) (int64, error) {
	account, err := m.account(ctx, transaction.AccountID)
	if err != nil {
		return 0, err
	}
	if transaction.Currency == "" {
		transaction.Currency = account.Currency
	}
	transaction.Currency = strings.ToUpper(transaction.Currency)
	if transaction.Currency != account.Currency {
		return 0, action.Invalid("流水币种必须与账户一致，不能自动换汇")
	}
	amount, err := ParseMoney(transaction.Amount)
	if err != nil {
		return 0, err
	}
	if amount == 0 {
		return 0, action.Invalid("流水金额不能为零")
	}
	if transaction.At == 0 {
		transaction.At = m.now().Unix()
	}
	if transaction.At < 0 || transaction.At > 253402300799 {
		return 0, action.Invalid("流水时间无效")
	}
	transaction.Category = strings.TrimSpace(transaction.Category)
	if transaction.Category == "" {
		transaction.Category = "未分类"
	}
	if utf8.RuneCountInString(transaction.Category) > 60 || utf8.RuneCountInString(transaction.Note) > 2000 || len(transaction.Ref) > 200 {
		return 0, action.Invalid("分类、备注或引用太长")
	}
	transaction.AccountName = account.Name
	transaction.Amount = FormatMoney(amount)
	return amount, nil
}
func (m *Module) SaveTransaction(ctx context.Context, transaction Transaction) (Transaction, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	amount, err := m.validateTransaction(ctx, &transaction)
	if err != nil {
		return transaction, err
	}
	var oldAccount string
	var oldAmount int64
	if transaction.ID > 0 {
		if err := m.DB.QueryRowContext(ctx, `SELECT account_id,amount_minor FROM finance_transactions WHERE id=?`, transaction.ID).Scan(&oldAccount, &oldAmount); errors.Is(err, sql.ErrNoRows) {
			return transaction, &action.Error{Status: 404, Message: "流水不存在"}
		} else if err != nil {
			return transaction, err
		}
	}
	delta := amount
	if oldAccount == transaction.AccountID {
		delta -= oldAmount
	} else if oldAccount != "" {
		if err := m.checkDelta(ctx, oldAccount, -oldAmount); err != nil {
			return transaction, err
		}
	}
	if err := m.checkDelta(ctx, transaction.AccountID, delta); err != nil {
		return transaction, err
	}
	if transaction.ID == 0 {
		result, err := m.DB.ExecContext(ctx, `INSERT INTO finance_transactions(account_id,at,amount_minor,currency,category,note,ref) VALUES(?,?,?,?,?,?,?)`, transaction.AccountID, transaction.At, amount, transaction.Currency, transaction.Category, transaction.Note, transaction.Ref)
		if err != nil {
			return transaction, err
		}
		transaction.ID, err = result.LastInsertId()
		return transaction, err
	}
	result, err := m.DB.ExecContext(ctx, `UPDATE finance_transactions SET account_id=?,at=?,amount_minor=?,currency=?,category=?,note=?,ref=?,import_key=CASE WHEN account_id=? THEN import_key ELSE NULL END WHERE id=?`, transaction.AccountID, transaction.At, amount, transaction.Currency, transaction.Category, transaction.Note, transaction.Ref, transaction.AccountID, transaction.ID)
	if err != nil {
		return transaction, err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return transaction, &action.Error{Status: 404, Message: "流水不存在"}
	}
	return transaction, nil
}
func (m *Module) DeleteTransaction(ctx context.Context, id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var accountID string
	var amount int64
	if err := m.DB.QueryRowContext(ctx, `SELECT account_id,amount_minor FROM finance_transactions WHERE id=?`, id).Scan(&accountID, &amount); errors.Is(err, sql.ErrNoRows) {
		return &action.Error{Status: 404, Message: "流水不存在"}
	} else if err != nil {
		return err
	}
	if err := m.checkDelta(ctx, accountID, -amount); err != nil {
		return err
	}
	result, err := m.DB.ExecContext(ctx, `DELETE FROM finance_transactions WHERE id=?`, id)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return &action.Error{Status: 404, Message: "流水不存在"}
	}
	return nil
}
func (m *Module) checkDelta(ctx context.Context, accountID string, delta int64) error {
	var opening, total int64
	if err := m.DB.QueryRowContext(ctx, `SELECT opening_minor,COALESCE((SELECT sum(amount_minor) FROM finance_transactions WHERE account_id=a.id),0) FROM finance_accounts a WHERE id=?`, accountID).Scan(&opening, &total); err != nil {
		return err
	}
	updated, err := addMoney(total, delta)
	if err != nil {
		return err
	}
	_, err = addMoney(opening, updated)
	return err
}
func (m *Module) SaveBudget(ctx context.Context, budget Budget) (Budget, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	budget.Category = strings.TrimSpace(budget.Category)
	if budget.Category == "" || utf8.RuneCountInString(budget.Category) > 60 {
		return budget, action.Invalid("预算分类须为 1–60 个字")
	}
	if _, _, err := m.month(ctx, budget.Period); err != nil || budget.Period == "" {
		return budget, action.Invalid("预算月份须为 YYYY-MM")
	}
	var err error
	budget.Currency, err = currency(budget.Currency)
	if err != nil {
		return budget, err
	}
	amount, err := ParseMoney(budget.Amount)
	if err != nil {
		return budget, err
	}
	if amount < 0 {
		return budget, action.Invalid("预算不能为负数")
	}
	budget.Spent = ""
	if budget.ID > 0 {
		var conflict int
		if err := m.DB.QueryRowContext(ctx, `SELECT count(*) FROM finance_budgets WHERE category=? AND period=? AND currency=? AND id<>?`, budget.Category, budget.Period, budget.Currency, budget.ID).Scan(&conflict); err != nil {
			return budget, err
		}
		if conflict > 0 {
			return budget, action.Invalid("这个分类、月份和币种已经有预算")
		}
		result, err := m.DB.ExecContext(ctx, `UPDATE finance_budgets SET category=?,period=?,amount_minor=?,currency=? WHERE id=?`, budget.Category, budget.Period, amount, budget.Currency, budget.ID)
		if err != nil {
			return budget, err
		}
		count, _ := result.RowsAffected()
		if count == 0 {
			return budget, &action.Error{Status: 404, Message: "预算不存在"}
		}
		budget.Amount = FormatMoney(amount)
		return budget, nil
	}
	_, err = m.DB.ExecContext(ctx, `INSERT INTO finance_budgets(category,period,amount_minor,currency) VALUES(?,?,?,?) ON CONFLICT(category,period,currency) DO UPDATE SET amount_minor=excluded.amount_minor`, budget.Category, budget.Period, amount, budget.Currency)
	if err != nil {
		return budget, err
	}
	err = m.DB.QueryRowContext(ctx, `SELECT id FROM finance_budgets WHERE category=? AND period=? AND currency=?`, budget.Category, budget.Period, budget.Currency).Scan(&budget.ID)
	budget.Amount = FormatMoney(amount)
	return budget, err
}
func (m *Module) Budgets(ctx context.Context, period string) ([]Budget, error) {
	start, end, err := m.month(ctx, period)
	if err != nil {
		return nil, err
	}
	rows, err := m.DB.QueryContext(ctx, `SELECT id,category,period,amount_minor,currency FROM finance_budgets WHERE period=? ORDER BY category,currency`, start.Format("2006-01"))
	if err != nil {
		return nil, err
	}
	list := []Budget{}
	for rows.Next() {
		var budget Budget
		var amount int64
		if err := rows.Scan(&budget.ID, &budget.Category, &budget.Period, &amount, &budget.Currency); err != nil {
			rows.Close()
			return nil, err
		}
		budget.Amount = FormatMoney(amount)
		list = append(list, budget)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for index := range list {
		budget := &list[index]
		amounts, err := m.DB.QueryContext(ctx, `SELECT currency,sum(amount_minor) FROM finance_transactions WHERE category=? AND at>=? AND at<? GROUP BY currency`, budget.Category, start.Unix(), end.Unix())
		if err != nil {
			return nil, err
		}
		totals := map[string]int64{}
		for amounts.Next() {
			var from string
			var amount int64
			if err := amounts.Scan(&from, &amount); err != nil {
				amounts.Close()
				return nil, err
			}
			totals[from] = amount
		}
		err = amounts.Err()
		amounts.Close()
		if err != nil {
			return nil, err
		}
		net, ok := model.ConvertReferenceTotals(totals, budget.Currency)
		if !ok {
			return nil, action.Invalid("预算折算超出范围")
		}
		if net < 0 {
			if net == (-1 << 63) {
				return nil, action.Invalid("预算合计超出范围")
			}
			net = -net
		} else {
			net = 0
		}
		budget.Spent = FormatMoney(net)
	}
	return list, nil
}
func (m *Module) DeleteBudget(ctx context.Context, id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	result, err := m.DB.ExecContext(ctx, `DELETE FROM finance_budgets WHERE id=?`, id)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return &action.Error{Status: 404, Message: "预算不存在"}
	}
	return nil
}
func (m *Module) netWorth(ctx context.Context) (Settings, []Account, int64, error) {
	config, err := m.Settings(ctx)
	if err != nil {
		return config, nil, 0, err
	}
	accounts, err := m.Accounts(ctx)
	if err != nil {
		return config, nil, 0, err
	}
	totals := map[string]int64{}
	for _, account := range accounts {
		minor, err := parseBalance(account.Balance)
		if err != nil {
			return config, nil, 0, err
		}
		totals[account.Currency], err = addMoney(totals[account.Currency], minor)
		if err != nil {
			return config, nil, 0, err
		}
	}
	total, ok := model.ConvertReferenceTotals(totals, config.Currency)
	if !ok {
		return config, nil, 0, action.Invalid("净资产折算超出范围")
	}
	return config, accounts, total, nil
}
func parseBalance(value string) (int64, error) {
	negative := strings.HasPrefix(value, "-")
	digits := strings.ReplaceAll(strings.TrimPrefix(value, "-"), ".", "")
	if negative {
		digits = "-" + digits
	}
	return strconv.ParseInt(digits, 10, 64)
}
func (m *Module) snapshot(ctx context.Context, replace bool) (*Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	config, accounts, net, err := m.netWorth(ctx)
	if err != nil {
		return nil, err
	}
	if len(accounts) == 0 {
		return nil, nil
	}
	location, err := time.LoadLocation(config.Timezone)
	if err != nil {
		return nil, err
	}
	now := m.now()
	day := now.In(location).Format("2006-01-02")
	balances, _ := json.Marshal(accounts)
	query := `INSERT INTO finance_snapshots(day,at,currency,net_minor,balances) VALUES(?,?,?,?,?) ON CONFLICT(day,currency) DO NOTHING`
	if replace {
		query = `INSERT INTO finance_snapshots(day,at,currency,net_minor,balances) VALUES(?,?,?,?,?) ON CONFLICT(day,currency) DO UPDATE SET at=excluded.at,net_minor=excluded.net_minor,balances=excluded.balances`
	}
	if _, err := m.DB.ExecContext(ctx, query, day, now.Unix(), config.Currency, net, string(balances)); err != nil {
		return nil, err
	}
	var snapshot Snapshot
	var stored int64
	var saved string
	err = m.DB.QueryRowContext(ctx, `SELECT day,at,currency,net_minor,balances FROM finance_snapshots WHERE day=? AND currency=?`, day, config.Currency).Scan(&snapshot.Day, &snapshot.At, &snapshot.Currency, &stored, &saved)
	snapshot.Balances = json.RawMessage(saved)
	snapshot.NetWorth = FormatMoney(stored)
	return &snapshot, err
}
func (m *Module) Snapshot(ctx context.Context) (*Snapshot, error) { return m.snapshot(ctx, true) }
func (m *Module) Jobs() []module.Job {
	return []module.Job{{Name: "finance.snapshot", Title: "每日账户余额快照", Every: time.Hour, Run: func(ctx context.Context) error { _, err := m.snapshot(ctx, false); return err }}}
}
func (m *Module) Snapshots(ctx context.Context) ([]Snapshot, error) {
	config, err := m.Settings(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := m.DB.QueryContext(ctx, `SELECT day,at,currency,net_minor,balances FROM finance_snapshots WHERE currency=? ORDER BY day DESC LIMIT 400`, config.Currency)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []Snapshot{}
	for rows.Next() {
		var snapshot Snapshot
		var amount int64
		var saved string
		if err := rows.Scan(&snapshot.Day, &snapshot.At, &snapshot.Currency, &amount, &saved); err != nil {
			return nil, err
		}
		snapshot.Balances = json.RawMessage(saved)
		snapshot.NetWorth = FormatMoney(amount)
		list = append(list, snapshot)
	}
	slices.Reverse(list)
	return list, rows.Err()
}
func (m *Module) Overview(ctx context.Context) ([]module.Card, error) {
	config, accounts, total, err := m.netWorth(ctx)
	if err != nil {
		return nil, err
	}
	value := "尚未记账"
	if len(accounts) > 0 {
		value = config.Currency + " " + FormatMoney(total)
	}
	return []module.Card{{ID: "finance", Title: "财务", Page: "finance", Value: value, Note: fmt.Sprintf("%d 个账户 · 跨币种为固定参考汇率", len(accounts))}}, nil
}
