package finance

import (
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/leeska/Lodestar/internal/action"
	"github.com/leeska/Lodestar/internal/model"
)

type Import struct {
	AccountID    string            `json:"account_id"`
	CSV          string            `json:"csv"`
	Columns      map[string]string `json:"columns"`
	DateFormat   string            `json:"date_format"`
	Delimiter    string            `json:"delimiter"`
	NumberFormat string            `json:"number_format"`
	Invert       bool              `json:"invert"`
	Deduplicate  *bool             `json:"deduplicate,omitempty"`
}
type ImportRow struct {
	Line        int         `json:"line"`
	Transaction Transaction `json:"transaction"`
	Duplicate   bool        `json:"duplicate"`
	key         string
}
type ImportResult struct {
	Rows     []ImportRow `json:"rows"`
	Total    int         `json:"total"`
	Skipped  int         `json:"skipped"`
	Imported int         `json:"imported"`
}

var englishAmount = regexp.MustCompile(`^[+-]?(?:[0-9]+|[0-9]{1,3}(?:,[0-9]{3})+)(?:\.[0-9]{1,2})?$`)
var europeanAmount = regexp.MustCompile(`^[+-]?(?:[0-9]+|[0-9]{1,3}(?:\.[0-9]{3})+)(?:,[0-9]{1,2})?$`)

func normalizeCSVAmount(value, format string) (string, error) {
	value = strings.TrimSpace(value)
	switch format {
	case "", "plain":
	case "en":
		if !englishAmount.MatchString(value) {
			return "", action.Invalid("金额不符合 1,234.56 格式")
		}
		value = strings.ReplaceAll(value, ",", "")
	case "eu":
		if !europeanAmount.MatchString(value) {
			return "", action.Invalid("金额不符合 1.234,56 格式")
		}
		value = strings.ReplaceAll(strings.ReplaceAll(value, ".", ""), ",", ".")
	default:
		return "", action.Invalid("数字格式须为 plain、en 或 eu")
	}
	amount, err := ParseMoney(value)
	if err != nil {
		return "", err
	}
	return FormatMoney(amount), nil
}
func (m *Module) prepareImport(ctx context.Context, input Import) ([]ImportRow, error) {
	if len(input.CSV) == 0 || len(input.CSV) > 1<<20 || !utf8.ValidString(input.CSV) {
		return nil, action.Invalid("请提供不超过 1 MB 的 UTF-8 CSV")
	}
	account, err := m.account(ctx, input.AccountID)
	if err != nil {
		return nil, err
	}
	config, err := m.Settings(ctx)
	if err != nil {
		return nil, err
	}
	location, err := time.LoadLocation(config.Timezone)
	if err != nil {
		return nil, err
	}
	delimiter := ','
	switch input.Delimiter {
	case "", ",":
	case ";":
		delimiter = ';'
	case "\t":
		delimiter = '\t'
	default:
		return nil, action.Invalid("分隔符须为逗号、分号或制表符")
	}
	reader := csv.NewReader(strings.NewReader(strings.TrimPrefix(input.CSV, "\ufeff")))
	reader.Comma = delimiter
	reader.FieldsPerRecord = -1
	header, err := reader.Read()
	if err != nil {
		return nil, action.Invalid("无法读取 CSV 表头")
	}
	headers := map[string]int{}
	for index, name := range header {
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, action.Invalid("CSV 表头不能留空")
		}
		if _, exists := headers[name]; exists {
			return nil, action.Invalid("CSV 表头不能重名")
		}
		headers[name] = index
	}
	for _, required := range []string{"at", "amount"} {
		if input.Columns[required] == "" {
			return nil, action.Invalid("请映射日期和金额两列")
		}
	}
	for field, name := range input.Columns {
		if field != "at" && field != "amount" && field != "category" && field != "note" && field != "ref" && field != "currency" {
			return nil, action.Invalid("不支持的映射字段")
		}
		if name != "" {
			if _, exists := headers[name]; !exists {
				return nil, action.Invalid("映射的列不在 CSV 表头中")
			}
		}
	}
	if input.DateFormat == "" {
		input.DateFormat = "2006-01-02"
	}
	if len(input.DateFormat) > 80 {
		return nil, action.Invalid("日期格式太长")
	}
	duplicates := input.Deduplicate == nil || *input.Deduplicate
	seen := map[string]int{}
	rows := []ImportRow{}
	for line := 2; ; line++ {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, action.Invalid("CSV 第 %d 行格式无效", line)
		}
		if len(record) != len(header) {
			return nil, action.Invalid("CSV 第 %d 行的列数不一致", line)
		}
		if len(rows) >= 2000 {
			return nil, action.Invalid("一次最多导入 2000 行")
		}
		value := func(field string) string {
			column := input.Columns[field]
			if column == "" {
				return ""
			}
			return strings.TrimSpace(record[headers[column]])
		}
		date, err := time.ParseInLocation(input.DateFormat, value("at"), location)
		if err != nil {
			return nil, action.Invalid("CSV 第 %d 行日期不符合所选格式", line)
		}
		amountText, err := normalizeCSVAmount(value("amount"), input.NumberFormat)
		if err != nil {
			return nil, action.Invalid("CSV 第 %d 行金额格式无效", line)
		}
		amount, _ := ParseMoney(amountText)
		if input.Invert {
			amount = -amount
		}
		transaction := Transaction{AccountID: account.ID, AccountName: account.Name, At: date.Unix(), Amount: FormatMoney(amount), Currency: account.Currency, Category: value("category"), Note: value("note"), Ref: value("ref")}
		if transaction.At < 0 || transaction.At > 253402300799 {
			return nil, action.Invalid("CSV 第 %d 行时间无效", line)
		}
		if amount == 0 {
			return nil, action.Invalid("CSV 第 %d 行金额为零", line)
		}
		if supplied := value("currency"); supplied != "" && strings.ToUpper(supplied) != account.Currency {
			return nil, action.Invalid("CSV 第 %d 行币种与账户不一致", line)
		}
		if transaction.Category == "" {
			transaction.Category = "未分类"
		}
		if utf8.RuneCountInString(transaction.Category) > 60 || utf8.RuneCountInString(transaction.Note) > 2000 || len(transaction.Ref) > 200 {
			return nil, action.Invalid("CSV 第 %d 行内容太长", line)
		}
		identity := transaction
		identity.AccountName = ""
		canonical, _ := json.Marshal(identity)
		digest := sha256.Sum256(canonical)
		fingerprint := hex.EncodeToString(digest[:])
		seen[fingerprint]++
		key := fmt.Sprintf("%s:%d", fingerprint, seen[fingerprint])
		row := ImportRow{Line: line, Transaction: transaction, key: key}
		if duplicates {
			var count int
			if err := m.DB.QueryRowContext(ctx, `SELECT count(*) FROM finance_transactions WHERE account_id=? AND import_key=?`, account.ID, key).Scan(&count); err != nil {
				return nil, err
			}
			row.Duplicate = count > 0
		} else {
			row.key = model.Secret()
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		return nil, action.Invalid("CSV 没有数据行")
	}
	return rows, nil
}
func (m *Module) PreviewImport(ctx context.Context, input Import) (ImportResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rows, err := m.prepareImport(ctx, input)
	if err != nil {
		return ImportResult{}, err
	}
	result := ImportResult{Total: len(rows), Rows: rows[:min(100, len(rows))]}
	for _, row := range rows {
		if row.Duplicate {
			result.Skipped++
		}
	}
	return result, nil
}
func (m *Module) Import(ctx context.Context, input Import) (ImportResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rows, err := m.prepareImport(ctx, input)
	if err != nil {
		return ImportResult{}, err
	}
	var delta int64
	for _, row := range rows {
		if !row.Duplicate {
			amount, _ := ParseMoney(row.Transaction.Amount)
			delta, err = addMoney(delta, amount)
			if err != nil {
				return ImportResult{}, err
			}
		}
	}
	if err := m.checkDelta(ctx, input.AccountID, delta); err != nil {
		return ImportResult{}, err
	}
	transaction, err := m.DB.BeginTx(ctx, nil)
	if err != nil {
		return ImportResult{}, err
	}
	defer transaction.Rollback()
	result := ImportResult{Total: len(rows), Rows: []ImportRow{}}
	for _, row := range rows {
		if row.Duplicate {
			result.Skipped++
			continue
		}
		item := row.Transaction
		amount, _ := ParseMoney(item.Amount)
		if _, err := transaction.ExecContext(ctx, `INSERT INTO finance_transactions(account_id,at,amount_minor,currency,category,note,ref,import_key) VALUES(?,?,?,?,?,?,?,?)`, item.AccountID, item.At, amount, item.Currency, item.Category, item.Note, item.Ref, row.key); err != nil {
			return ImportResult{}, err
		}
		result.Imported++
	}
	if err := transaction.Commit(); err != nil {
		return ImportResult{}, err
	}
	return result, nil
}
