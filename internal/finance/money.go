package finance

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/leeska/Lodestar/internal/action"
)

var moneyPattern = regexp.MustCompile(`^[+-]?[0-9]{1,14}(?:\.[0-9]{1,2})?$`)

func ParseMoney(value string) (int64, error) {
	value = strings.TrimSpace(value)
	if !moneyPattern.MatchString(value) {
		return 0, action.Invalid("金额须为最多 14 位整数、2 位小数的十进制字符串")
	}
	negative := strings.HasPrefix(value, "-")
	value = strings.TrimPrefix(strings.TrimPrefix(value, "-"), "+")
	whole, fraction, _ := strings.Cut(value, ".")
	fraction = (fraction + "00")[:2]
	integer, _ := strconv.ParseInt(whole, 10, 64)
	decimal, _ := strconv.ParseInt(fraction, 10, 64)
	amount := integer*100 + decimal
	if negative {
		amount = -amount
	}
	return amount, nil
}
func FormatMoney(amount int64) string {
	negative := amount < 0
	magnitude := uint64(amount)
	if negative {
		magnitude = uint64(-(amount + 1)) + 1
	}
	sign := ""
	if negative {
		sign = "-"
	}
	return fmt.Sprintf("%s%d.%02d", sign, magnitude/100, magnitude%100)
}
func addMoney(first, second int64) (int64, error) {
	if (second > 0 && first > math.MaxInt64-second) || (second < 0 && first < math.MinInt64-second) {
		return 0, action.Invalid("合计金额超过可处理范围")
	}
	return first + second, nil
}
