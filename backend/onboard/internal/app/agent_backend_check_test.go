package app

import "bike/onboard/internal/agenttools"

// 编译期检查：App 的适配器满足接入智能体工具后端接口。
var _ agenttools.Backend = AgentBackend{}
