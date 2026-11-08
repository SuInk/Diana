package agent

import (
	"context"
	"errors"
	"testing"
	"time"
)

func withFastNetworkChangeRetry(t *testing.T) {
	t.Helper()
	old := networkChangeRetryDelays
	networkChangeRetryDelays = []time.Duration{time.Millisecond, time.Millisecond}
	t.Cleanup(func() { networkChangeRetryDelays = old })
}

func TestNavigateRetryingNetworkChangeRecovers(t *testing.T) {
	withFastNetworkChangeRetry(t)
	calls := 0
	err := navigateRetryingNetworkChange(context.Background(), func(context.Context) (string, bool, error) {
		calls++
		if calls < 3 {
			return chromeNetworkChangedError, false, nil
		}
		return "", false, nil
	})
	if err != nil {
		t.Fatalf("网络抖动后第三次导航成功，不该报错：%v", err)
	}
	if calls != 3 {
		t.Fatalf("导航次数 = %d，想要 3", calls)
	}
}

func TestNavigateRetryingNetworkChangeGivesUp(t *testing.T) {
	withFastNetworkChangeRetry(t)
	calls := 0
	err := navigateRetryingNetworkChange(context.Background(), func(context.Context) (string, bool, error) {
		calls++
		return chromeNetworkChangedError, false, nil
	})
	if err == nil || err.Error() != chromeNetworkChangedError {
		t.Fatalf("一直网络变化应原样报出 Chrome 的错误，得到 %v", err)
	}
	if calls != 1+len(networkChangeRetryDelays) {
		t.Fatalf("导航次数 = %d，想要 %d", calls, 1+len(networkChangeRetryDelays))
	}
}

func TestNavigateRetryingNetworkChangeOtherErrorsNotRetried(t *testing.T) {
	withFastNetworkChangeRetry(t)
	cases := []struct {
		name      string
		errorText string
		download  bool
		err       error
		want      string
	}{
		{"域名解析失败", "net::ERR_NAME_NOT_RESOLVED", false, nil, "net::ERR_NAME_NOT_RESOLVED"},
		{"变成下载", "", true, nil, "browser navigation became a download"},
		{"CDP 调用失败", "", false, errors.New("target closed"), "target closed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			err := navigateRetryingNetworkChange(context.Background(), func(context.Context) (string, bool, error) {
				calls++
				return tc.errorText, tc.download, tc.err
			})
			if err == nil || err.Error() != tc.want {
				t.Fatalf("err = %v，想要 %q", err, tc.want)
			}
			if calls != 1 {
				t.Fatalf("非网络变化的错误不该重试，导航了 %d 次", calls)
			}
		})
	}
}

func TestNavigateRetryingNetworkChangeStopsWhenContextDone(t *testing.T) {
	old := networkChangeRetryDelays
	networkChangeRetryDelays = []time.Duration{time.Hour}
	t.Cleanup(func() { networkChangeRetryDelays = old })
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	err := navigateRetryingNetworkChange(ctx, func(context.Context) (string, bool, error) {
		calls++
		cancel()
		return chromeNetworkChangedError, false, nil
	})
	if err == nil || err.Error() != chromeNetworkChangedError {
		t.Fatalf("期限到了应报出原错误，得到 %v", err)
	}
	if calls != 1 {
		t.Fatalf("期限到了还在重试，导航了 %d 次", calls)
	}
}
