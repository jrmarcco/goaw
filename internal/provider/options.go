package provider

import "github.com/jrmarcco/jit/xbean/option"

// Opt 是 Provider 构造函数的可选参数。
type Opt = option.Opt[providerOpts]

type providerOpts struct {
	baseURL string
}

// WithBaseURL 设置自定义 API 端点，用于接入代理或兼容网关。
func WithBaseURL(baseURL string) Opt {
	return func(o *providerOpts) {
		o.baseURL = baseURL
	}
}
