package brief

import (
	"net/http"

	"github.com/leeska/Lodestar/internal/action"
	"github.com/leeska/Lodestar/internal/httpjson"
	"github.com/leeska/Lodestar/internal/module"
)

const base = "/api/v1/admin/brief"

func (m *Module) Routes(routes module.Routes) {
	routes.Admin("GET "+base+"/settings", func(w http.ResponseWriter, r *http.Request) {
		config, err := m.Settings(r.Context())
		if err != nil {
			httpjson.Fail(w, err)
			return
		}
		httpjson.Write(w, 200, config)
	})
	routes.Admin("PUT "+base+"/settings", func(w http.ResponseWriter, r *http.Request) {
		var config Settings
		if !httpjson.Decode(w, r, &config) {
			return
		}
		if err := m.Save(r.Context(), config); err != nil {
			fail(w, err)
			return
		}
		httpjson.Write(w, 200, config)
	})
	routes.Admin("GET "+base+"/preview", func(w http.ResponseWriter, r *http.Request) {
		document, err := m.Preview(r.Context())
		if err != nil {
			fail(w, err)
			return
		}
		httpjson.Write(w, 200, document)
	})
	routes.Admin("POST "+base+"/send", func(w http.ResponseWriter, r *http.Request) {
		document, err := m.Send(r.Context(), false)
		if err != nil {
			fail(w, err)
			return
		}
		httpjson.Write(w, 200, document)
	})
}

func fail(w http.ResponseWriter, err error) {
	if typed, ok := action.AsError(err); ok {
		httpjson.Error(w, typed.Status, typed.Message)
	} else {
		httpjson.Fail(w, err)
	}
}

func (*Module) Actions() []action.Action {
	return []action.Action{
		action.R("brief.preview", action.Read, "GET", base+"/preview", "预览简报", "汇总当前状况，不发送通知。已配置助理且启用改写时会调用模型，失败使用原始数据版。", nil),
		action.R("brief.send_now", action.Write, "POST", base+"/send", "立即发送简报", "通过已配置的通知出口发送一份简报，并记录到时间线。", nil),
	}
}
