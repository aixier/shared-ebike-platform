// Package adapters 厂商适配器框架（桩接入规范第 1、6 节）。
// 每个厂商一个子目录，把厂商报文译成标准桩消息；适配器之外的代码不知道厂商。
// 新增厂商：在本目录下加子包，并在 builtin 里登记一行；启用与否由本市配置 PILE_ADAPTERS 决定。
package adapters

import (
	"fmt"
	"strings"

	"bike/site/adapters/acme"
	"bike/site/internal/pilemsg"
)

// Adapter 一个厂商的适配器
type Adapter interface {
	// Vendor 厂商名：MQTT 命名空间 vendor/{厂商}/#，HTTP 回调 /device/v1/vendor/{厂商}
	Vendor() string
	// TranslateMQTT 译厂商 MQTT 主题与报文
	TranslateMQTT(topic string, payload []byte) ([]pilemsg.Message, error)
	// TranslateHTTP 译厂商 HTTP 回调报文
	TranslateHTTP(body []byte) ([]pilemsg.Message, error)
}

// builtin 编译进本服务的适配器
var builtin = map[string]func() Adapter{
	acme.Vendor: func() Adapter { return acme.New() },
}

// Enabled 按配置启用适配器；配置里出现未知厂商时报错，避免静默漏接
func Enabled(names []string) (map[string]Adapter, error) {
	out := map[string]Adapter{}
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		mk, ok := builtin[n]
		if !ok {
			return nil, fmt.Errorf("未知的厂商适配器 %q", n)
		}
		out[n] = mk()
	}
	return out, nil
}
