package finance

import (
	"context"
	"encoding/json"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leeska/Lodestar/internal/action"
	"github.com/leeska/Lodestar/internal/assets"
	"github.com/leeska/Lodestar/internal/model"
	"github.com/leeska/Lodestar/internal/module"
	"github.com/leeska/Lodestar/internal/storage"
	"github.com/leeska/Lodestar/internal/vault"
)

func fixture(t *testing.T) (*Module, *storage.Store) {
	t.Helper()
	mod := &Module{Now: func() time.Time { return time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC) }}
	store, err := storage.OpenWith(filepath.Join(t.TempDir(), "finance.db"), module.Migrations([]module.Module{&vault.Module{}, &assets.Module{}, mod}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	secrets, err := vault.New(store.DB(), make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	mod.DB, mod.Assets = store.DB(), &assets.Store{DB: store.DB(), Vault: secrets}
	if err := mod.SaveSettings(context.Background(), Settings{Currency: "CNY", Timezone: "UTC"}); err != nil {
		t.Fatal(err)
	}
	return mod, store
}
func newAccount(t *testing.T, mod *Module, opening string) Account {
	t.Helper()
	account, err := mod.SaveAccount(context.Background(), Account{Name: "记账账户", Kind: "bank", Currency: "CNY", OpeningBalance: opening})
	if err != nil {
		t.Fatal(err)
	}
	return account
}
func TestMoneyIsExactAndDoesNotAcceptFloatingNotation(t *testing.T) {
	for input, want := range map[string]int64{"0.10": 10, "0.20": 20, "-0.08": -8, "+12.3": 1230, "99999999999999.99": 9999999999999999} {
		got, err := ParseMoney(input)
		if err != nil || got != want {
			t.Fatal(input, got, err)
		}
	}
	for _, input := range []string{"1e3", "NaN", "Infinity", "1.234", "1,000.00", "100000000000000", "--1", ""} {
		if _, err := ParseMoney(input); err == nil {
			t.Fatal("invalid money accepted", input)
		}
	}
	if FormatMoney(math.MinInt64) != "-92233720368547758.08" {
		t.Fatal("negative aggregate formatting overflow")
	}
	if _, err := addMoney(math.MaxInt64, 1); err == nil {
		t.Fatal("overflow silently accepted")
	}
	value, err := parseBalance("0.08")
	if err != nil || value != 8 {
		t.Fatal("aggregate balance parsed in a nondecimal base", value, err)
	}
}
func TestLedgerManualBalancesAndBudgetRefunds(t *testing.T) {
	mod, _ := fixture(t)
	ctx := context.Background()
	account := newAccount(t, mod, "0.10")
	income, err := mod.SaveTransaction(ctx, Transaction{AccountID: account.ID, Amount: "0.20", Category: "收入"})
	if err != nil {
		t.Fatal(err)
	}
	expense, err := mod.SaveTransaction(ctx, Transaction{AccountID: account.ID, Amount: "-0.08", Category: "餐饮"})
	if err != nil {
		t.Fatal(err)
	}
	balance, err := mod.account(ctx, account.ID)
	if err != nil || balance.Balance != "0.22" {
		t.Fatal(balance, err)
	}
	if _, err := mod.SaveTransaction(ctx, Transaction{AccountID: account.ID, Amount: "0.03", Category: "餐饮"}); err != nil {
		t.Fatal(err)
	}
	budget, err := mod.SaveBudget(ctx, Budget{Category: "餐饮", Period: "2026-11", Currency: "CNY", Amount: "0.50"})
	if err != nil {
		t.Fatal(err)
	}
	list, err := mod.Budgets(ctx, "2026-11")
	if err != nil || len(list) != 1 || list[0].Spent != "0.05" {
		t.Fatal(list, err)
	}
	budget.Category = "饮食"
	if _, err := mod.SaveBudget(ctx, budget); err != nil {
		t.Fatal(err)
	}
	list, _ = mod.Budgets(ctx, "2026-11")
	if len(list) != 1 || list[0].Category != "饮食" {
		t.Fatal("editing budget created another budget", list)
	}
	income.Amount = "0.21"
	if _, err := mod.SaveTransaction(ctx, income); err != nil {
		t.Fatal(err)
	}
	second := newAccount(t, mod, "1.00")
	expense.AccountID = second.ID
	if _, err := mod.SaveTransaction(ctx, expense); err != nil {
		t.Fatal(err)
	}
	firstBalance, _ := mod.account(ctx, account.ID)
	secondBalance, _ := mod.account(ctx, second.ID)
	if firstBalance.Balance != "0.34" || secondBalance.Balance != "0.92" {
		t.Fatal(firstBalance, secondBalance)
	}
	account.Currency = "USD"
	if _, err := mod.SaveAccount(ctx, account); err == nil {
		t.Fatal("currency changed under existing transactions")
	}
	account.Currency = "CNY"
	account.BalanceMode = "manual"
	account.ManualBalance = "12.34"
	manual, err := mod.SaveAccount(ctx, account)
	if err != nil || manual.Balance != "12.34" {
		t.Fatal(manual, err)
	}
	summary, err := mod.Summary(ctx, "2026-11")
	if err != nil || summary.Inflow != "0.24" || summary.Outflow != "0.08" || summary.Net != "0.16" || summary.Count != 3 {
		t.Fatal(summary, err)
	}
	if err := mod.DeleteAccount(ctx, account.ID); err == nil {
		t.Fatal("nonempty account deleted")
	}
	if err := mod.DeleteTransaction(ctx, expense.ID); err != nil {
		t.Fatal(err)
	}
	if err := mod.DeleteAccount(ctx, second.ID); err != nil {
		t.Fatal(err)
	}
}
func TestCSVMappingAtomicFailureDedupAndRename(t *testing.T) {
	mod, _ := fixture(t)
	ctx := context.Background()
	account := newAccount(t, mod, "100.00")
	input := Import{AccountID: account.ID, CSV: "日期,金额,备注\n2026-11-01,-0.10,咖啡\n2026-11-01,-0.10,咖啡\n2026-11-02,0.20,退款", Columns: map[string]string{"at": "日期", "amount": "金额", "note": "备注"}}
	preview, err := mod.PreviewImport(ctx, input)
	if err != nil || preview.Total != 3 || preview.Skipped != 0 {
		t.Fatal(preview, err)
	}
	transactions, _ := mod.Transactions(ctx, Query{})
	if len(transactions) != 0 {
		t.Fatal("preview wrote to ledger")
	}
	imported, err := mod.Import(ctx, input)
	if err != nil || imported.Imported != 3 {
		t.Fatal(imported, err)
	}
	account.Name = "改名的账户"
	if _, err := mod.SaveAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	again, err := mod.Import(ctx, input)
	if err != nil || again.Imported != 0 || again.Skipped != 3 {
		t.Fatal("rename defeated duplicate detection", again, err)
	}
	broken := input
	broken.CSV = "日期,金额,备注\n2026-11-03,-1.00,尚未导入的新行\n2026-11-04,not-money,坏行"
	if _, err := mod.Import(ctx, broken); err == nil {
		t.Fatal("bad CSV partially accepted")
	}
	transactions, _ = mod.Transactions(ctx, Query{})
	if len(transactions) != 3 {
		t.Fatal("partial CSV import", transactions)
	}
	input.Delimiter = ";"
	input.NumberFormat = "eu"
	input.Invert = true
	input.CSV = "日期;金额;备注\n2026-11-03;1.234,56;欧洲金额"
	if result, err := mod.Import(ctx, input); err != nil || result.Imported != 1 {
		t.Fatal(result, err)
	}
	balance, _ := mod.account(ctx, account.ID)
	if balance.Balance != "-1134.56" {
		t.Fatal(balance)
	}
	if _, err := normalizeCSVAmount("1,23", "en"); err == nil {
		t.Fatal("invalid thousands grouping silently changed amount")
	}
}
func TestSnapshotsDailyOnceAndManualReplacement(t *testing.T) {
	mod, _ := fixture(t)
	ctx := context.Background()
	account := newAccount(t, mod, "0.08")
	now := time.Date(2026, 11, 15, 17, 0, 0, 0, time.UTC)
	mod.Now = func() time.Time { return now }
	if err := mod.SaveSettings(ctx, Settings{Currency: "CNY", Timezone: "Asia/Singapore"}); err != nil {
		t.Fatal(err)
	}
	first, err := mod.snapshot(ctx, false)
	if err != nil || first.Day != "2026-11-16" || first.NetWorth != "0.08" || !json.Valid(first.Balances) {
		t.Fatal(first, err)
	}
	account.OpeningBalance = "0.20"
	if _, err := mod.SaveAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	same, err := mod.snapshot(ctx, false)
	if err != nil || same.NetWorth != "0.08" {
		t.Fatal("daily snapshot overwritten", same, err)
	}
	replaced, err := mod.Snapshot(ctx)
	if err != nil || replaced.NetWorth != "0.20" {
		t.Fatal(replaced, err)
	}
	now = now.AddDate(0, 0, 1)
	if _, err := mod.snapshot(ctx, false); err != nil {
		t.Fatal(err)
	}
	list, err := mod.Snapshots(ctx)
	if err != nil || len(list) != 2 {
		t.Fatal(list, err)
	}
}
func TestNetWorthRoundsCombinedCurrencyBalancesOnce(t *testing.T) {
	mod, _ := fixture(t)
	ctx := context.Background()
	for range 3 {
		if _, err := mod.SaveAccount(ctx, Account{Name: "韩元", Kind: "cash", Currency: "KRW", OpeningBalance: "1.00"}); err != nil {
			t.Fatal(err)
		}
	}
	_, _, total, err := mod.netWorth(ctx)
	if err != nil || total != 2 {
		t.Fatal("per-account rounding distorted net worth", total, err)
	}
}

type fakeNodes []model.NodeView

func (nodes fakeNodes) Nodes(context.Context, time.Time) ([]model.NodeView, error) { return nodes, nil }
func TestRecurringCostsPreserveUnknownsAndSourceScopes(t *testing.T) {
	mod, _ := fixture(t)
	ctx := context.Background()
	for _, asset := range []assets.Asset{
		{Name: "月费", Kind: "subscription", Cost: 12, Currency: "USD", Period: "月"},
		{Name: "未知", Kind: "subscription", Currency: "CNY", Period: "月"},
		{Name: "一次性", Kind: "device", Cost: 100, Currency: "CNY", Period: "一次性"},
	} {
		if _, err := mod.Assets.Save(ctx, asset, mod.now()); err != nil {
			t.Fatal(err)
		}
	}
	mod.Nodes = fakeNodes{{Node: model.Node{ID: "paid", Name: "付费节点", Billing: model.Billing{Price: 10, Currency: "EUR", PricePeriod: "年"}}}, {Node: model.Node{ID: "free", Name: "免费节点", Billing: model.Billing{Free: true}}}}
	result, err := mod.Recurring(ctx)
	if err != nil || result.Monthly != "91.61" || result.Unknown != 1 || len(result.Items) != 4 {
		t.Fatal(result, err)
	}
	scoped := action.WithActor(ctx, action.Actor{Name: "limited", MaxRisk: action.Read, Modules: []string{"finance"}})
	limited, err := mod.Recurring(scoped)
	if err != nil || len(limited.Items) != 0 || len(limited.Omitted) != 2 {
		t.Fatal("source scopes bypassed", limited, err)
	}
}
func TestOverflowIsRejectedBeforeLedgerMutation(t *testing.T) {
	mod, store := fixture(t)
	ctx := context.Background()
	account := newAccount(t, mod, "0")
	if _, err := store.DB().Exec(`INSERT INTO finance_transactions(account_id,at,amount_minor,currency,category) VALUES(?,1,?,'CNY','seed')`, account.ID, int64(math.MaxInt64-100)); err != nil {
		t.Fatal(err)
	}
	if _, err := mod.SaveTransaction(ctx, Transaction{AccountID: account.ID, Amount: "1.01"}); err == nil {
		t.Fatal("overflowing transaction accepted")
	}
	input := Import{AccountID: account.ID, CSV: "date,amount\n2026-11-15,1.01", Columns: map[string]string{"at": "date", "amount": "amount"}}
	if _, err := mod.Import(ctx, input); err == nil {
		t.Fatal("overflowing CSV accepted")
	}
	var count int
	if err := store.DB().QueryRow(`SELECT count(*) FROM finance_transactions`).Scan(&count); err != nil || count != 1 {
		t.Fatal("overflow mutated ledger", count, err)
	}
}
func TestCSVRejectsCurrencyMismatchAndInvalidUTF8(t *testing.T) {
	mod, _ := fixture(t)
	account := newAccount(t, mod, "0")
	input := Import{AccountID: account.ID, CSV: "date,amount,currency\n2026-11-15,1.00,USD", Columns: map[string]string{"at": "date", "amount": "amount", "currency": "currency"}}
	if _, err := mod.Import(context.Background(), input); err == nil {
		t.Fatal("CSV implicitly exchanged currency")
	}
	input.CSV = strings.Repeat("x", 1<<20+1)
	if _, err := mod.PreviewImport(context.Background(), input); err == nil {
		t.Fatal("oversized CSV accepted")
	}
	input.CSV = string([]byte{255})
	if _, err := mod.PreviewImport(context.Background(), input); err == nil {
		t.Fatal("invalid UTF-8 accepted")
	}
}

func TestUnconfiguredFinanceUsesPortableTimezone(t *testing.T) {
	mod, store := fixture(t)
	if _, err := store.DB().Exec(`DELETE FROM finance_settings`); err != nil {
		t.Fatal(err)
	}
	config, err := mod.Settings(context.Background())
	if err != nil || config.Timezone != "UTC" {
		t.Fatal("default timezone cannot be passed to browser Intl", config, err)
	}
}
