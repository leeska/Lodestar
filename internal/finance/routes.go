package finance

import (
	"encoding/csv"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/leeska/Lodestar/internal/action"
	"github.com/leeska/Lodestar/internal/httpjson"
	"github.com/leeska/Lodestar/internal/model"
	"github.com/leeska/Lodestar/internal/module"
)

const base = "/api/v1/admin/finance"

func fail(w http.ResponseWriter, err error) {
	if typed, ok := action.AsError(err); ok {
		httpjson.Error(w, typed.Status, typed.Message)
	} else {
		httpjson.Fail(w, err)
	}
}
func idFrom(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		httpjson.Error(w, 400, "id 无效")
		return 0, false
	}
	return id, true
}
func (m *Module) Routes(routes module.Routes) {
	routes.Admin("GET "+base+"/summary", func(w http.ResponseWriter, r *http.Request) {
		summary, err := m.Summary(r.Context(), r.URL.Query().Get("month"))
		if err != nil {
			fail(w, err)
			return
		}
		httpjson.Write(w, 200, summary)
	})
	routes.Admin("GET "+base+"/settings", func(w http.ResponseWriter, r *http.Request) {
		config, err := m.Settings(r.Context())
		if err != nil {
			fail(w, err)
			return
		}
		start, _, err := m.month(r.Context(), "")
		if err != nil {
			fail(w, err)
			return
		}
		httpjson.Write(w, 200, map[string]any{"settings": config, "currencies": model.PriceUnits, "current_month": start.Format("2006-01")})
	})
	routes.Admin("PUT "+base+"/settings", func(w http.ResponseWriter, r *http.Request) {
		var config Settings
		if !httpjson.Decode(w, r, &config) {
			return
		}
		if err := m.SaveSettings(r.Context(), config); err != nil {
			fail(w, err)
			return
		}
		httpjson.Write(w, 200, config)
	})
	routes.Admin("GET "+base+"/accounts", func(w http.ResponseWriter, r *http.Request) {
		config, list, total, err := m.netWorth(r.Context())
		if err != nil {
			fail(w, err)
			return
		}
		httpjson.Write(w, 200, map[string]any{"accounts": list, "currency": config.Currency, "net_worth": FormatMoney(total)})
	})
	for _, method := range []string{"POST", "PUT"} {
		path := base + "/accounts"
		if method == "PUT" {
			path += "/{id}"
		}
		routes.Admin(method+" "+path, func(w http.ResponseWriter, r *http.Request) {
			var account Account
			if !httpjson.Decode(w, r, &account) {
				return
			}
			account.ID = r.PathValue("id")
			saved, err := m.SaveAccount(r.Context(), account)
			if err != nil {
				fail(w, err)
				return
			}
			httpjson.Write(w, 200, saved)
		})
	}
	routes.Admin("DELETE "+base+"/accounts/{id}", func(w http.ResponseWriter, r *http.Request) {
		if err := m.DeleteAccount(r.Context(), r.PathValue("id")); err != nil {
			fail(w, err)
			return
		}
		w.WriteHeader(204)
	})
	routes.Admin("GET "+base+"/transactions", func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		var before int64
		var limit int
		var err error
		if query.Get("before") != "" {
			before, err = strconv.ParseInt(query.Get("before"), 10, 64)
			if err != nil || before < 0 {
				httpjson.Error(w, 400, "before 无效")
				return
			}
		}
		if query.Get("limit") != "" {
			limit, err = strconv.Atoi(query.Get("limit"))
			if err != nil || limit < 1 || limit > 500 {
				httpjson.Error(w, 400, "limit 须为 1–500")
				return
			}
		}
		list, err := m.Transactions(r.Context(), Query{AccountID: query.Get("account"), Category: query.Get("category"), Month: query.Get("month"), Before: before, Limit: limit})
		if err != nil {
			fail(w, err)
			return
		}
		httpjson.Write(w, 200, map[string]any{"transactions": list})
	})
	for _, method := range []string{"POST", "PUT"} {
		path := base + "/transactions"
		if method == "PUT" {
			path += "/{id}"
		}
		routes.Admin(method+" "+path, func(w http.ResponseWriter, r *http.Request) {
			var transaction Transaction
			if !httpjson.Decode(w, r, &transaction) {
				return
			}
			transaction.ID = 0
			if method == "PUT" {
				id, ok := idFrom(w, r)
				if !ok {
					return
				}
				transaction.ID = id
			}
			saved, err := m.SaveTransaction(r.Context(), transaction)
			if err != nil {
				fail(w, err)
				return
			}
			httpjson.Write(w, 200, saved)
		})
	}
	routes.Admin("DELETE "+base+"/transactions/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := idFrom(w, r)
		if !ok {
			return
		}
		if err := m.DeleteTransaction(r.Context(), id); err != nil {
			fail(w, err)
			return
		}
		w.WriteHeader(204)
	})
	routes.Admin("GET "+base+"/budgets", func(w http.ResponseWriter, r *http.Request) {
		list, err := m.Budgets(r.Context(), r.URL.Query().Get("month"))
		if err != nil {
			fail(w, err)
			return
		}
		httpjson.Write(w, 200, map[string]any{"budgets": list})
	})
	routes.Admin("POST "+base+"/budgets", func(w http.ResponseWriter, r *http.Request) {
		var budget Budget
		if !httpjson.Decode(w, r, &budget) {
			return
		}
		budget.ID = 0
		saved, err := m.SaveBudget(r.Context(), budget)
		if err != nil {
			fail(w, err)
			return
		}
		httpjson.Write(w, 200, saved)
	})
	routes.Admin("PUT "+base+"/budgets/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := idFrom(w, r)
		if !ok {
			return
		}
		var budget Budget
		if !httpjson.Decode(w, r, &budget) {
			return
		}
		budget.ID = id
		saved, err := m.SaveBudget(r.Context(), budget)
		if err != nil {
			fail(w, err)
			return
		}
		httpjson.Write(w, 200, saved)
	})
	routes.Admin("DELETE "+base+"/budgets/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := idFrom(w, r)
		if !ok {
			return
		}
		if err := m.DeleteBudget(r.Context(), id); err != nil {
			fail(w, err)
			return
		}
		w.WriteHeader(204)
	})
	routes.Admin("GET "+base+"/recurring", func(w http.ResponseWriter, r *http.Request) {
		result, err := m.Recurring(r.Context())
		if err != nil {
			fail(w, err)
			return
		}
		httpjson.Write(w, 200, result)
	})
	routes.Admin("GET "+base+"/snapshots", func(w http.ResponseWriter, r *http.Request) {
		list, err := m.Snapshots(r.Context())
		if err != nil {
			fail(w, err)
			return
		}
		for index := range list {
			list[index].Balances = nil
		}
		httpjson.Write(w, 200, map[string]any{"snapshots": list})
	})
	routes.Admin("POST "+base+"/snapshot", func(w http.ResponseWriter, r *http.Request) {
		snapshot, err := m.Snapshot(r.Context())
		if err != nil {
			fail(w, err)
			return
		}
		httpjson.Write(w, 200, map[string]any{"snapshot": snapshot})
	})
	routes.Admin("POST "+base+"/csv/columns", func(w http.ResponseWriter, r *http.Request) {
		var input Import
		if !httpjson.DecodeLimit(w, r, &input, 8<<20) {
			return
		}
		if len(input.CSV) == 0 || len(input.CSV) > 1<<20 || !utf8.ValidString(input.CSV) {
			httpjson.Error(w, 400, "请提供不超过 1 MB 的 UTF-8 CSV")
			return
		}
		reader := csv.NewReader(strings.NewReader(strings.TrimPrefix(input.CSV, "\ufeff")))
		switch input.Delimiter {
		case "", ",":
		case ";":
			reader.Comma = ';'
		case "\t":
			reader.Comma = '\t'
		default:
			httpjson.Error(w, 400, "分隔符无效")
			return
		}
		headers, err := reader.Read()
		if err != nil {
			httpjson.Error(w, 400, "无法解析 CSV 表头")
			return
		}
		seen := map[string]bool{}
		for index := range headers {
			headers[index] = strings.TrimSpace(headers[index])
			if headers[index] == "" || seen[headers[index]] {
				httpjson.Error(w, 400, "CSV 表头不能留空或重复")
				return
			}
			seen[headers[index]] = true
		}
		httpjson.Write(w, 200, map[string]any{"columns": headers})
	})
	for _, mode := range []string{"preview", "import"} {
		routes.Admin("POST "+base+"/csv/"+mode, func(w http.ResponseWriter, r *http.Request) {
			var input Import
			if !httpjson.DecodeLimit(w, r, &input, 8<<20) {
				return
			}
			var result ImportResult
			var err error
			if mode == "preview" {
				result, err = m.PreviewImport(r.Context(), input)
			} else {
				result, err = m.Import(r.Context(), input)
			}
			if err != nil {
				fail(w, err)
				return
			}
			httpjson.Write(w, 200, result)
		})
	}
}

func (*Module) Actions() []action.Action {
	accountID := action.Str("账户 id")
	transactionID := action.Int("流水 id")
	account := action.Body("name、kind（cash/bank/investment/liability/other）、currency、opening_balance（十进制字符串）、balance_mode（ledger/manual）、manual_balance；正数为资产，负债填负数。修改前先读并保留未改字段")
	transaction := action.Body("account_id、at（Unix 秒，默认现在）、amount（十进制字符串，收入正数、支出负数）、currency（须与账户一致）、category、note、ref")
	importBody := action.Body("account_id、csv（UTF-8，最多 1 MB/2000 行）、columns（at/amount/category/note/ref/currency 到原始列名的映射）、date_format（Go 时间布局，默认 2006-01-02）、delimiter（逗号/分号/制表符）、number_format（plain/en/eu）、invert（反转金额符号）、deduplicate（默认 true）；先预览再导入")
	return []action.Action{
		action.R("finance.summary", action.Read, "GET", base+"/summary", "月度收支", "汇总指定月份账本的流入、流出和净流入；按固定参考汇率估算。", map[string]any{"month": action.Str("YYYY-MM")}, "month"),
		action.R("finance.settings", action.Read, "GET", base+"/settings", "财务设置", "读取汇总币种和时区。", nil),
		action.R("finance.update_settings", action.Write, "PUT", base+"/settings", "修改财务设置", "设置 currency 和 timezone，不改变原币种金额。", map[string]any{"body": action.Body("currency、timezone（IANA）")}),
		action.R("finance.accounts", action.Read, "GET", base+"/accounts", "账户与净资产", "列出账户余额和按固定参考汇率估算的净资产。", nil),
		action.R("finance.add_account", action.Write, "POST", base+"/accounts", "添加记账账户", "创建本地账户，不连接银行。", map[string]any{"body": account}),
		action.R("finance.update_account", action.Write, "PUT", base+"/accounts/{id}", "修改记账账户", "修改账户与余额模式，有流水时不能换币种。", map[string]any{"id": accountID, "body": account}),
		action.R("finance.delete_account", action.Danger, "DELETE", base+"/accounts/{id}", "删除记账账户", "永久删除空账户；有流水时拒绝删除。", map[string]any{"id": accountID}),
		action.R("finance.transactions", action.Read, "GET", base+"/transactions", "记账流水", "按账户、分类、月份和游标查询本地流水。", map[string]any{"account": accountID, "category": action.Str("分类"), "month": action.Str("YYYY-MM"), "before": transactionID, "limit": action.Int("1–500，默认 200")}, "account", "category", "month", "before", "limit"),
		action.R("finance.add_transaction", action.Write, "POST", base+"/transactions", "记录流水", "只写入本地账本，不执行支付或转账。", map[string]any{"body": transaction}),
		action.R("finance.update_transaction", action.Write, "PUT", base+"/transactions/{id}", "修改流水", "更正本地账本记录。", map[string]any{"id": transactionID, "body": transaction}),
		action.R("finance.delete_transaction", action.Danger, "DELETE", base+"/transactions/{id}", "删除流水", "永久删除记账记录，不能恢复。", map[string]any{"id": transactionID}),
		action.R("finance.budgets", action.Read, "GET", base+"/budgets", "月度预算", "读取预算与扣除同分类退款后的净支出。", map[string]any{"month": action.Str("YYYY-MM")}, "month"),
		action.R("finance.set_budget", action.Write, "POST", base+"/budgets", "设置预算", "按分类、月份和币种创建或更新预算。", map[string]any{"body": action.Body("category、period（YYYY-MM）、amount（非负十进制字符串）、currency")}),
		action.R("finance.update_budget", action.Write, "PUT", base+"/budgets/{id}", "修改预算", "修改现有预算，包括分类、月份、币种和金额。", map[string]any{"id": action.Int("预算 id"), "body": action.Body("category、period（YYYY-MM）、amount（非负十进制字符串）、currency")}),
		action.R("finance.delete_budget", action.Danger, "DELETE", base+"/budgets/{id}", "删除预算", "删除本地预算记录。", map[string]any{"id": action.Int("预算 id")}),
		action.R("finance.recurring", action.Read, "GET", base+"/recurring", "每月固定支出", "只读汇总资产和节点费用，按 30 天估算；仅包含调用者有权读取的来源，不自动记流水。", nil),
		action.R("finance.snapshots", action.Read, "GET", base+"/snapshots", "净资产曲线", "最近 400 个当前汇总币种的日快照。", nil),
		action.R("finance.snapshot", action.Write, "POST", base+"/snapshot", "更新今日快照", "主动更新今天的余额快照，日常任务每天只记一次。", nil),
		action.R("finance.csv_columns", action.Read, "POST", base+"/csv/columns", "读取 CSV 表头", "读取用户提供文件的列名，不保存。", map[string]any{"body": action.Body("csv、delimiter")}),
		action.R("finance.csv_preview", action.Read, "POST", base+"/csv/preview", "预览 CSV 导入", "完整校验，返回前 100 行及重复行数，不写账本。", map[string]any{"body": importBody}),
		action.R("finance.csv_import", action.Write, "POST", base+"/csv/import", "导入 CSV 账本", "整体校验和事务写入，单行错误不部分导入。", map[string]any{"body": importBody}),
	}
}
