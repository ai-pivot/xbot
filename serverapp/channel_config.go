package serverapp

import (
	"fmt"
	"strconv"

	"xbot/config"
)

// getChannelConfigs reads channel configurations from the config file.
// Extracted from DirectBackend.GetChannelConfigs for direct use by RPCTable.
func getChannelConfigs() (map[string]map[string]string, error) {
	cfg := config.LoadFromFile(config.ConfigFilePath())
	if cfg == nil {
		return nil, fmt.Errorf("config not found")
	}
	result := make(map[string]map[string]string)
	result["web"] = map[string]string{
		"enabled": strconv.FormatBool(cfg.Web.Enable),
		"host":    cfg.Web.Host,
		"port":    strconv.Itoa(cfg.Web.Port),
	}
	result["feishu"] = map[string]string{
		"enabled":            strconv.FormatBool(cfg.Feishu.Enabled),
		"app_id":             cfg.Feishu.AppID,
		"app_secret":         cfg.Feishu.AppSecret,
		"encrypt_key":        cfg.Feishu.EncryptKey,
		"verification_token": cfg.Feishu.VerificationToken,
		"domain":             cfg.Feishu.Domain,
	}
	result["qq"] = map[string]string{
		"enabled":       strconv.FormatBool(cfg.QQ.Enabled),
		"app_id":        cfg.QQ.AppID,
		"client_secret": cfg.QQ.ClientSecret,
	}
	result["napcat"] = map[string]string{
		"enabled": strconv.FormatBool(cfg.NapCat.Enabled),
		"ws_url":  cfg.NapCat.WSUrl,
		"token":   cfg.NapCat.Token,
	}
	return result, nil
}

// setChannelConfig writes a channel's configuration values to the config file.
// If reconfigureFn is non-nil, it is called after the config is saved.
// Extracted from DirectBackend.SetChannelConfig for direct use by RPCTable.
func setChannelConfig(ch string, values map[string]string, reconfigureFn func(string)) error {
	cfg := config.LoadFromFile(config.ConfigFilePath())
	if cfg == nil {
		cfg = &config.Config{}
	}
	switch ch {
	case "web":
		if v, ok := values["enabled"]; ok {
			cfg.Web.Enable, _ = strconv.ParseBool(v)
		} else if v, ok := values["enable"]; ok {
			cfg.Web.Enable, _ = strconv.ParseBool(v)
		}
		if v, ok := values["host"]; ok {
			cfg.Web.Host = v
		}
		if v, ok := values["port"]; ok {
			cfg.Web.Port, _ = strconv.Atoi(v)
		}
	case "feishu":
		if v, ok := values["enabled"]; ok {
			cfg.Feishu.Enabled, _ = strconv.ParseBool(v)
		}
		if v, ok := values["app_id"]; ok {
			cfg.Feishu.AppID = v
		}
		if v, ok := values["app_secret"]; ok {
			cfg.Feishu.AppSecret = v
		}
		if v, ok := values["encrypt_key"]; ok {
			cfg.Feishu.EncryptKey = v
		}
		if v, ok := values["verification_token"]; ok {
			cfg.Feishu.VerificationToken = v
		}
		if v, ok := values["domain"]; ok {
			cfg.Feishu.Domain = v
		}
	case "qq":
		if v, ok := values["enabled"]; ok {
			cfg.QQ.Enabled, _ = strconv.ParseBool(v)
		}
		if v, ok := values["app_id"]; ok {
			cfg.QQ.AppID = v
		}
		if v, ok := values["client_secret"]; ok {
			cfg.QQ.ClientSecret = v
		}
	case "napcat":
		if v, ok := values["enabled"]; ok {
			cfg.NapCat.Enabled, _ = strconv.ParseBool(v)
		}
		if v, ok := values["ws_url"]; ok {
			cfg.NapCat.WSUrl = v
		}
		if v, ok := values["token"]; ok {
			cfg.NapCat.Token = v
		}
	default:
		return fmt.Errorf("unknown channel: %s", ch)
	}
	if err := config.SaveToFile(config.ConfigFilePath(), cfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	if reconfigureFn != nil {
		reconfigureFn(ch)
	}
	return nil
}
