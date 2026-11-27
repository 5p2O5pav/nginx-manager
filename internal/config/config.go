// Package config defines and validates the desired proxy configuration.
package config

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

type Config struct {
	Version  int      `json:"version"`
	Domains  []Domain `json:"domains"`
	Settings Settings `json:"settings,omitempty"`
}

type Settings struct {
	// Zero selects the default: unlimited uploads and 3600 seconds.
	MaxUploadMB         int `json:"max_upload_mb,omitempty"`
	ProxyTimeoutSeconds int `json:"proxy_timeout_seconds,omitempty"`
}

type Domain struct {
	Name    string  `json:"domain"`
	Enabled bool    `json:"enabled"`
	Routes  []Route `json:"routes"`
}

type Route struct {
	Path        string       `json:"path"`
	Target      string       `json:"target"`
	StripPrefix bool         `json:"strip_prefix"`
	WebSocket   bool         `json:"websocket"`
	UpstreamTLS *UpstreamTLS `json:"upstream_tls,omitempty"`
}

type UpstreamTLS struct {
	ServerName         string `json:"server_name,omitempty"`
	CAFile             string `json:"ca_file,omitempty"`
	InsecureSkipVerify bool   `json:"insecure_skip_verify,omitempty"`
}

var label = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
var pathPattern = regexp.MustCompile(`^/[A-Za-z0-9._~/-]*$`)

func DomainName(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if len(s) > 253 || net.ParseIP(s) != nil {
		return "", fmt.Errorf("无效域名: %q", s)
	}
	parts := strings.Split(s, ".")
	if len(parts) < 2 {
		return "", fmt.Errorf("域名必须包含至少两部分: %q", s)
	}
	for _, part := range parts {
		if !label.MatchString(part) {
			return "", fmt.Errorf("无效域名: %q", s)
		}
	}
	return s, nil
}

func NormalizeRoute(r Route) (Route, error) {
	r.Path = strings.TrimSpace(r.Path)
	if r.Path == "" {
		r.Path = "/"
	}
	if !pathPattern.MatchString(r.Path) || strings.Contains(r.Path, "//") {
		return r, fmt.Errorf("无效路径: %q", r.Path)
	}
	for _, segment := range strings.Split(r.Path, "/") {
		if segment == "." || segment == ".." {
			return r, fmt.Errorf("路径包含非法段: %q", r.Path)
		}
	}
	r.Target = strings.TrimSpace(r.Target)
	if !strings.Contains(r.Target, "://") {
		r.Target = "http://" + r.Target
	}
	u, err := url.Parse(r.Target)
	if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return r, fmt.Errorf("无效后端地址: %q", r.Target)
	}
	host := u.Hostname()
	if net.ParseIP(host) == nil {
		if _, err := DomainName(host); err != nil && !label.MatchString(strings.ToLower(host)) {
			return r, fmt.Errorf("无效后端主机: %q", host)
		}
	} else if strings.Contains(host, ":") && !strings.HasPrefix(u.Host, "[") {
		return r, fmt.Errorf("IPv6 地址必须使用方括号")
	}
	if port := u.Port(); port != "" {
		p, err := strconv.Atoi(port)
		if err != nil || p < 1 || p > 65535 {
			return r, fmt.Errorf("无效端口: %q", port)
		}
	}
	if strings.HasSuffix(u.Host, ":") {
		return r, fmt.Errorf("端口不能为空")
	}
	if r.UpstreamTLS != nil {
		if u.Scheme != "https" {
			return r, fmt.Errorf("仅 HTTPS 后端支持 TLS 设置")
		}
		name := r.UpstreamTLS.ServerName
		if name != "" && net.ParseIP(name) == nil {
			canonical, err := DomainName(name)
			if err != nil {
				return r, fmt.Errorf("无效上游 TLS 名称: %w", err)
			}
			r.UpstreamTLS.ServerName = canonical
		}
		if strings.ContainsAny(r.UpstreamTLS.CAFile, "\x00\r\n;{}\"\\ ") {
			return r, fmt.Errorf("无效 CA 路径")
		}
	}
	u.Path = ""
	r.Target = u.String()
	return r, nil
}

func (c *Config) Normalize() error {
	if c.Version != 1 {
		return fmt.Errorf("不支持配置版本 %d", c.Version)
	}
	if c.Settings.MaxUploadMB < 0 || c.Settings.MaxUploadMB > 1048576 {
		return fmt.Errorf("上传大小设置无效")
	}
	if c.Settings.ProxyTimeoutSeconds < 0 || c.Settings.ProxyTimeoutSeconds > 86400 {
		return fmt.Errorf("代理超时设置无效")
	}
	seen := map[string]bool{}
	for i := range c.Domains {
		d := &c.Domains[i]
		name, err := DomainName(d.Name)
		if err != nil {
			return err
		}
		d.Name = name
		if seen[name] {
			return fmt.Errorf("域名重复: %s", name)
		}
		seen[name] = true
		paths := map[string]bool{}
		for j := range d.Routes {
			r, err := NormalizeRoute(d.Routes[j])
			if err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			if paths[r.Path] {
				return fmt.Errorf("%s: 路径重复: %s", name, r.Path)
			}
			paths[r.Path] = true
			d.Routes[j] = r
		}
	}
	return nil
}
