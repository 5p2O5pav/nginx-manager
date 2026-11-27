// Package nginx renders complete, self-contained Nginx configurations.
package nginx

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"nginx-manager/internal/config"
)

type Paths struct {
	SSLDir     string
	Webroot    string
	DefaultDir string
	CABundle   string
}

// Certificate checks the key pair, name and validity rather than trusting stored state.
func Certificate(dir, domain string, now time.Time) error {
	_, err := CertificateInfo(dir, domain, now)
	return err
}

func CertificateInfo(dir, domain string, now time.Time) (*x509.Certificate, error) {
	cert, err := tls.LoadX509KeyPair(filepath.Join(dir, domain, "fullchain.pem"), filepath.Join(dir, domain, "key.pem"))
	if err != nil {
		return nil, err
	}
	if len(cert.Certificate) == 0 {
		return nil, fmt.Errorf("证书链为空")
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return nil, err
	}
	if now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) {
		return leaf, fmt.Errorf("证书不在有效期内")
	}
	return leaf, leaf.VerifyHostname(domain)
}

func safePath(s string) bool { return path.IsAbs(s) && !strings.ContainsAny(s, "\x00\r\n;{}\"\\ $") }

// Render validates input before rendering. A domain with no usable certificate
// only receives an HTTP-01 server; it never exposes its proxy routes.
func Render(c config.Config, p Paths, now time.Time) (string, error) {
	if err := c.Normalize(); err != nil {
		return "", err
	}
	for _, s := range []string{p.SSLDir, p.Webroot, p.DefaultDir} {
		if !safePath(s) {
			return "", fmt.Errorf("无效 Nginx 路径: %q", s)
		}
	}
	var b strings.Builder
	b.WriteString("user www-data;\nworker_processes auto;\npid /run/nginx.pid;\nerror_log /var/log/nginx/error.log;\nevents { worker_connections 1024; }\nhttp {\n include /etc/nginx/mime.types;\n default_type application/octet-stream;\n")
	if c.Settings.MaxUploadMB == 0 {
		b.WriteString(" client_max_body_size 0;\n")
	} else {
		fmt.Fprintf(&b, " client_max_body_size %dm;\n", c.Settings.MaxUploadMB)
	}
	b.WriteString(" map $http_upgrade $nm_connection_upgrade { default upgrade; '' close; }\n")
	timeout := c.Settings.ProxyTimeoutSeconds
	if timeout == 0 {
		timeout = 3600
	}
	b.WriteString(" server { listen 80 default_server; listen [::]:80 default_server; server_name _; return 444; }\n server { listen 443 ssl default_server; listen [::]:443 ssl default_server; server_name _; ssl_reject_handshake on; return 421; }\n")
	for _, d := range c.Domains {
		if !d.Enabled {
			continue
		}
		usable := Certificate(p.SSLDir, d.Name, now) == nil
		fmt.Fprintf(&b, " server {\n  listen 80; listen [::]:80; server_name %s;\n  location ^~ /.well-known/acme-challenge/ { root %s; try_files $uri =404; }\n", d.Name, p.Webroot)
		if usable {
			fmt.Fprintf(&b, "  location / { return 301 https://%s$request_uri; }\n", d.Name)
		} else {
			b.WriteString("  location / { return 444; }\n")
		}
		b.WriteString(" }\n")
		if !usable {
			continue
		}
		fmt.Fprintf(&b, " server {\n  listen 443 ssl; listen [::]:443 ssl; server_name %s;\n  ssl_certificate %s/%s/fullchain.pem;\n  ssl_certificate_key %s/%s/key.pem;\n  if ($ssl_server_name != %s) { return 421; }\n", d.Name, p.SSLDir, d.Name, p.SSLDir, d.Name, d.Name)
		hasRoot := false
		for _, r := range d.Routes {
			if r.Path == "/" {
				hasRoot = true
			}
			fmt.Fprintf(&b, "  location %s {\n", r.Path)
			b.WriteString("   proxy_set_header Host $host;\n   proxy_set_header X-Real-IP $remote_addr;\n   proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;\n   proxy_set_header X-Forwarded-Proto $scheme;\n   proxy_http_version 1.1;\n")
			fmt.Fprintf(&b, "   proxy_read_timeout %ds;\n   proxy_send_timeout %ds;\n", timeout, timeout)
			if r.WebSocket {
				b.WriteString("   proxy_set_header Upgrade $http_upgrade;\n   proxy_set_header Connection $nm_connection_upgrade;\n")
			} else {
				b.WriteString("   proxy_set_header Connection \"\";\n")
			}
			u, _ := url.Parse(r.Target)
			if u.Scheme == "https" {
				name := u.Hostname()
				verify := true
				ca := p.CABundle
				if r.UpstreamTLS != nil {
					if r.UpstreamTLS.ServerName != "" {
						name = r.UpstreamTLS.ServerName
					}
					verify = !r.UpstreamTLS.InsecureSkipVerify
					if r.UpstreamTLS.CAFile != "" {
						ca = r.UpstreamTLS.CAFile
					}
				}
				fmt.Fprintf(&b, "   proxy_ssl_server_name on;\n   proxy_ssl_name %s;\n", name)
				if verify {
					if !safePath(ca) {
						return "", fmt.Errorf("未设置有效的 CA 文件")
					}
					if _, err := os.Stat(ca); err != nil {
						return "", fmt.Errorf("CA 文件不可用: %w", err)
					}
					fmt.Fprintf(&b, "   proxy_ssl_verify on;\n   proxy_ssl_trusted_certificate %s;\n", ca)
				} else {
					b.WriteString("   proxy_ssl_verify off;\n")
				}
			}
			if r.StripPrefix && r.Path != "/" {
				// Literal prefix is validated and escaped before use in a regex.
				prefix := regexQuote(r.Path)
				if !strings.HasSuffix(r.Path, "/") {
					prefix += "/?"
				}
				fmt.Fprintf(&b, "   rewrite ^%s(.*)$ /$1 break;\n", prefix)
			}
			fmt.Fprintf(&b, "   proxy_pass %s;\n  }\n", r.Target)
		}
		if !hasRoot {
			fmt.Fprintf(&b, "  location / { root %s; try_files /index.html =404; }\n", p.DefaultDir)
		}
		b.WriteString(" }\n")
	}
	b.WriteString("}\n")
	return b.String(), nil
}

func regexQuote(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(`.\+*?()|[]{}^$`, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}
