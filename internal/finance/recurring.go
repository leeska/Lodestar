package finance

import (
	"context"
	"math"
	"math/big"
	"strconv"

	"github.com/leeska/Lodestar/internal/action"
	"github.com/leeska/Lodestar/internal/assets"
	"github.com/leeska/Lodestar/internal/model"
)

type Recurring struct {
	Ref      string  `json:"ref"`
	Name     string  `json:"name"`
	Cost     string  `json:"cost"`
	Currency string  `json:"currency"`
	Period   string  `json:"period"`
	Monthly  *string `json:"monthly"`
}
type RecurringResult struct {
	Items    []Recurring `json:"items"`
	Currency string      `json:"currency"`
	Monthly  string      `json:"monthly"`
	Unknown  int         `json:"unknown"`
	Omitted  []string    `json:"omitted"`
}

var periodDays = map[string]int64{"周": 7, "月": 30, "季": 90, "半年": 180, "年": 365, "两年": 730, "三年": 1095}

func canRead(ctx context.Context, name string) bool {
	actor, exists := action.ActorFrom(ctx)
	return !exists || actor.Allows(action.Action{Name: name + ".list", Risk: action.Read})
}
func (m *Module) Recurring(ctx context.Context) (RecurringResult, error) {
	config, err := m.Settings(ctx)
	if err != nil {
		return RecurringResult{}, err
	}
	result := RecurringResult{Items: []Recurring{}, Currency: config.Currency, Omitted: []string{}, Monthly: "0.00"}
	var total int64
	appendItem := func(ref, name string, cost float64, from, period string, free bool) error {
		if period == "一次性" {
			return nil
		}
		if from == "" {
			from = "CNY"
		}
		item := Recurring{Ref: ref, Name: name, Currency: from, Period: period}
		var monthly int64
		known := free
		if free {
			item.Cost = "0.00"
		} else if !math.IsNaN(cost) && !math.IsInf(cost, 0) && cost >= 0 && cost <= 1e9 {
			item.Cost = strconv.FormatFloat(cost, 'f', 2, 64)
			minor, parseErr := ParseMoney(item.Cost)
			sourceRate, sourceOK := model.ReferenceFXCNY[from]
			targetRate, targetOK := model.ReferenceFXCNY[config.Currency]
			days := periodDays[period]
			if parseErr == nil && cost > 0 && days > 0 && sourceOK && targetOK {
				numerator := new(big.Int).Mul(big.NewInt(minor), big.NewInt(30*sourceRate))
				monthly, known = model.RoundRatio(numerator, new(big.Int).Mul(big.NewInt(days), big.NewInt(targetRate)))
			}
		}
		if known {
			value := FormatMoney(monthly)
			item.Monthly = &value
			var err error
			total, err = addMoney(total, monthly)
			if err != nil {
				return err
			}
		} else {
			result.Unknown++
		}
		result.Items = append(result.Items, item)
		return nil
	}
	if canRead(ctx, "assets") && m.Assets != nil {
		list, err := m.Assets.List(ctx, assets.Filter{}, m.now())
		if err != nil {
			return result, err
		}
		for _, asset := range list {
			if asset.Status != "active" {
				continue
			}
			if err := appendItem("asset:"+asset.ID, asset.Name, asset.Cost, asset.Currency, asset.Period, false); err != nil {
				return result, err
			}
		}
	} else if !canRead(ctx, "assets") {
		result.Omitted = append(result.Omitted, "assets")
	}
	if canRead(ctx, "nodes") && m.Nodes != nil {
		list, err := m.Nodes.Nodes(ctx, m.now())
		if err != nil {
			return result, err
		}
		for _, node := range list {
			if err := appendItem("node:"+node.ID, node.Name, node.Billing.Price, node.Billing.Currency, node.Billing.PricePeriod, node.Billing.Free); err != nil {
				return result, err
			}
		}
	} else if !canRead(ctx, "nodes") {
		result.Omitted = append(result.Omitted, "nodes")
	}
	result.Monthly = FormatMoney(total)
	return result, nil
}
