package docker

import "github.com/leeska/Lodestar/internal/action"

// Actions are the Docker capabilities.
func (m *Module) Actions() []action.Action {
	node := action.Str("节点 id，来自 docker.nodes")
	container := action.Str("容器 id 或名字，来自 docker.node")
	const base = "/api/v1/admin/docker/nodes/{node}"
	return []action.Action{
		action.R("docker.nodes", action.Read, "GET", "/api/v1/admin/docker", "Docker 节点", "列出可以管理 Docker 的节点。", nil),
		action.R("docker.node", action.Read, "GET", base, "节点上的容器", "读取节点上的容器、镜像与 Compose 项目。", map[string]any{"node": node}),
		action.R("docker.container_action", action.Write, "POST", base+"/containers/{id}/{action}", "启停容器", "启动、停止或重启容器。",
			map[string]any{"node": node, "id": container, "action": map[string]any{"type": "string", "enum": []string{"start", "stop", "restart"}}}),
		action.R("docker.remove_container", action.Danger, "DELETE", base+"/containers/{id}", "删除容器", "删除容器（正在运行的会被强制停止）。", map[string]any{"node": node, "id": container}),
		action.R("docker.remove_image", action.Danger, "DELETE", base+"/images/{id}", "删除镜像", "删除镜像。", map[string]any{"node": node, "id": action.Str("镜像 id")}),
		action.R("docker.compose_file", action.Read, "GET", base+"/compose/{project}", "读取 Compose 文件", "读取 Compose 项目的 compose 文件。", map[string]any{"node": node, "project": action.Str("项目名")}),
		action.R("docker.save_compose", action.Write, "PUT", base+"/compose/{project}", "保存 Compose 文件", "保存（或新建）Compose 项目的 compose 文件，不会自动启动。",
			map[string]any{"node": node, "project": action.Str("项目名：小写字母、数字、- 和 _"), "body": action.Body("{\"content\": compose 文件内容}")}),
	}
}
