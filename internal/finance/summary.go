package finance

import (
	"context"

	"github.com/leeska/Lodestar/internal/action"
	"github.com/leeska/Lodestar/internal/model"
)

type Summary struct {
	Month    string `json:"month"`
	Currency string `json:"currency"`
	Inflow   string `json:"inflow"`
	Outflow  string `json:"outflow"`
	Net      string `json:"net"`
	Count    int    `json:"count"`
}

func (m *Module) Summary(ctx context.Context, month string) (Summary, error) {
	config, err := m.Settings(ctx)
	if err != nil {
		return Summary{}, err
	}
	start, end, err := m.month(ctx, month)
	if err != nil {
		return Summary{}, err
	}
	result := Summary{Month: start.Format("2006-01"), Currency: config.Currency}
	rows, err := m.DB.QueryContext(ctx, `SELECT currency,amount_minor>0,sum(amount_minor),count(*) FROM finance_transactions WHERE at>=? AND at<? GROUP BY currency,amount_minor>0`, start.Unix(), end.Unix())
	if err != nil {
		return result, err
	}
	defer rows.Close()
	incoming, outgoing, net := map[string]int64{}, map[string]int64{}, map[string]int64{}
	for rows.Next() {
		var currency string
		var positive bool
		var amount int64
		var count int
		if err := rows.Scan(&currency, &positive, &amount, &count); err != nil {
			return result, err
		}
		if positive {
			incoming[currency] = amount
		} else {
			if amount == (-1 << 63) {
				return result, action.Invalid("流水合计超出范围")
			}
			outgoing[currency] = -amount
		}
		net[currency], err = addMoney(net[currency], amount)
		if err != nil {
			return result, err
		}
		result.Count += count
	}
	if err := rows.Err(); err != nil {
		return result, err
	}
	inflow, firstOK := model.ConvertReferenceTotals(incoming, config.Currency)
	outflow, secondOK := model.ConvertReferenceTotals(outgoing, config.Currency)
	balance, thirdOK := model.ConvertReferenceTotals(net, config.Currency)
	if !firstOK || !secondOK || !thirdOK {
		return result, action.Invalid("流水折算超出范围")
	}
	result.Inflow, result.Outflow, result.Net = FormatMoney(inflow), FormatMoney(outflow), FormatMoney(balance)
	return result, nil
}
