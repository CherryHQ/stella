package qq

import pkgchannel "github.com/CherryHQ/stella/pkg/channel"

type QQConfig struct {
	InstanceID   string `json:"-"`
	AppID        string `json:"app_id"`
	AppSecret    string `json:"app_secret"`
	EnableNotify bool   `json:"enable_notify"`
}

func DecodeConfig(raw map[string]any) (QQConfig, error) {
	return pkgchannel.DecodePluginConfig[QQConfig](raw, "qq")
}
