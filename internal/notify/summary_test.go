package notify

import (
	"strings"
	"testing"

	"github.com/leeska/Lodestar/internal/model"
)

func TestSummaryUsesItsOwnMessageWithoutAlertState(t *testing.T) {
	message := telegramMessage(model.Event{Kind: model.EventBrief, NodeName: "每日简报", Message: strings.Repeat("状况正常", 2000)})
	if strings.Contains(message, "告警解除") || strings.Contains(message, "事件编号") || !strings.Contains(message, "完整内容见时间线") || utf16Len(message) > telegramLimit {
		t.Fatal("summary was formatted as an alert or exceeded the channel limit")
	}
}
