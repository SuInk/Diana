package agent

import (
	"context"
	"errors"
	"time"
)

// chromeNetworkChangedError 是 Chrome 在导航途中发现系统网络配置变了（网卡、路由、
// DNS 变动，代理/VPN 重连时常见）就把请求整个放弃时给的错误。它只说明那一刻的
// 连接作废了，不说明网站打不开；Chrome 自己也建议直接重试。
const chromeNetworkChangedError = "net::ERR_NETWORK_CHANGED"

// networkChangeRetryDelays 是遇到 ERR_NETWORK_CHANGED 后每次重新导航前等的时间。
// 网络抖动通常是一阵，等一小会儿让 Chrome 换好新的网络状态再来。
var networkChangeRetryDelays = []time.Duration{500 * time.Millisecond, 1500 * time.Millisecond}

// navigateRetryingNetworkChange 执行一次导航；导航因为系统网络变化被 Chrome 放弃时，
// 在同一个页面里稍等再导航，最多重试 len(networkChangeRetryDelays) 次。其它错误照旧
// 直接返回。navigate 返回 Chrome 的 errorText、是否变成下载以及 CDP 调用本身的错误。
func navigateRetryingNetworkChange(ctx context.Context, navigate func(context.Context) (string, bool, error)) error {
	for attempt := 0; ; attempt++ {
		errorText, isDownload, err := navigate(ctx)
		if err != nil {
			return err
		}
		if isDownload {
			return errors.New("browser navigation became a download")
		}
		if errorText == "" {
			return nil
		}
		if errorText != chromeNetworkChangedError || attempt >= len(networkChangeRetryDelays) {
			return errors.New(errorText)
		}
		timer := time.NewTimer(networkChangeRetryDelays[attempt])
		select {
		case <-ctx.Done():
			timer.Stop()
			return errors.New(errorText)
		case <-timer.C:
		}
	}
}
