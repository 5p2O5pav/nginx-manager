//go:build linux

package manager

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"nginx-manager/internal/config"
)

const acmeBinary = "/root/.acme.sh/acme.sh"
const acmeStage = "/etc/nm/acme"

// Certificate operations that invoke acme.sh use a per-domain lock in
// addition to the configuration lock. The deployment hook does not take it,
// since acme.sh can invoke the hook synchronously during installation.
func lockCertificate(name string) (*os.File, error) {
	dir := filepath.Join(root, "acme-locks")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, name+".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := lockFile(f); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func unlockCertificate(f *os.File) { unlockFile(f); f.Close() }

// Issue runs without the manager lock because acme.sh invokes the deployment
// hook synchronously; the hook takes the lock before touching active files.
func Issue(name string) error {
	name, err := config.DomainName(name)
	if err != nil {
		return err
	}
	lock, err := lockCertificate(name)
	if err != nil {
		return err
	}
	defer unlockCertificate(lock)
	m, err := Open()
	if err != nil {
		return err
	}
	c, err := m.Read()
	if err != nil {
		m.Close()
		return err
	}
	found := false
	for _, d := range c.Domains {
		if d.Name == name && d.Enabled {
			found = true
			break
		}
	}
	if !found {
		m.Close()
		return fmt.Errorf("域名不存在或未启用: %s", name)
	}
	if err := m.Reload(); err != nil {
		m.Close()
		return err
	}
	m.Close()
	if _, err := os.Stat(acmeBinary); err != nil {
		return fmt.Errorf("未找到 ACME.sh (%s): %w", acmeBinary, err)
	}
	if _, err := os.Stat("/usr/local/bin/nm"); err != nil {
		return fmt.Errorf("续签回调需要 /usr/local/bin/nm: %w", err)
	}
	stage := filepath.Join(acmeStage, name)
	cmd := exec.Command(acmeBinary, "--issue", "--server", "letsencrypt", "--keylength", "ec-256", "-d", name, "-w", paths.Webroot)
	if out, err := cmd.CombinedOutput(); err != nil {
		var exit *exec.ExitError
		// acme.sh uses exit status 2 for a certificate not yet due for renewal.
		if !errors.As(err, &exit) || exit.ExitCode() != 2 {
			return fmt.Errorf("签发失败，请检查域名 DNS 和公网 80 端口: %w\n%s", err, strings.TrimSpace(string(out)))
		}
	}
	if err := os.MkdirAll(stage, 0700); err != nil {
		return fmt.Errorf("证书已签发，创建暂存目录失败: %w", err)
	}
	// The name is strictly validated, and the executable path is fixed; neither
	// contains shell metacharacters. acme.sh runs its reloadcmd through a shell.
	hook := "/usr/local/bin/nm cert deploy " + name
	if err := command(acmeBinary, "--install-cert", "--ecc", "-d", name, "--fullchain-file", filepath.Join(stage, "fullchain.pem"), "--key-file", filepath.Join(stage, "key.pem"), "--reloadcmd", hook); err != nil {
		return fmt.Errorf("证书已签发，但安装或部署失败，证书已保留: %w", err)
	}
	// Some acme.sh versions do not execute the hook on initial installation.
	if err := Deploy(name); err != nil {
		return fmt.Errorf("证书已签发，但部署失败，证书已保留: %w", err)
	}
	return nil
}

// Deploy installs a complete staged key pair under the global lock. The old
// pair is restored if testing or applying the new configuration fails.
func Deploy(name string) error {
	name, err := config.DomainName(name)
	if err != nil {
		return err
	}
	m, err := Open()
	if err != nil {
		return err
	}
	defer m.Close()
	c, err := m.Read()
	if err != nil {
		return err
	}
	found := false
	for _, d := range c.Domains {
		if d.Name == name && d.Enabled {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("域名不存在或未启用: %s", name)
	}
	stage, target := filepath.Join(acmeStage, name), filepath.Join(paths.SSLDir, name)
	chain, err := os.ReadFile(filepath.Join(stage, "fullchain.pem"))
	if err != nil {
		return err
	}
	key, err := os.ReadFile(filepath.Join(stage, "key.pem"))
	if err != nil {
		return err
	}
	pair, err := tls.X509KeyPair(chain, key)
	if err != nil {
		return fmt.Errorf("待部署证书和私钥不匹配: %w", err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return fmt.Errorf("待部署证书无效: %w", err)
	}
	now := time.Now()
	if now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) {
		return fmt.Errorf("待部署证书不在有效期内")
	}
	if err := leaf.VerifyHostname(name); err != nil {
		return fmt.Errorf("待部署证书域名不匹配: %w", err)
	}
	if err := os.MkdirAll(target, 0700); err != nil {
		return err
	}
	return m.apply(c, []fileChange{
		{Path: filepath.Join(target, "fullchain.pem"), Data: chain, Mode: 0644},
		{Path: filepath.Join(target, "key.pem"), Data: key, Mode: 0600},
	})
}

// DeleteCertificate removes the ACME renewal entry and disables HTTPS before
// removing the installed and staged key pairs. Callers must obtain confirmation.
func DeleteCertificate(name string) error {
	name, err := config.DomainName(name)
	if err != nil {
		return err
	}
	lock, err := lockCertificate(name)
	if err != nil {
		return err
	}
	defer unlockCertificate(lock)
	return deleteCertificateLocked(name)
}

func deleteCertificateLocked(name string) error {
	m, err := Open()
	if err != nil {
		return err
	}
	defer m.Close()
	c, err := m.Read()
	if err != nil {
		return err
	}
	found := false
	for _, d := range c.Domains {
		if d.Name == name {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("域名不存在: %s", name)
	}
	stage := filepath.Join(acmeStage, name)
	if _, err := os.Stat(stage); err == nil {
		conf := filepath.Join("/root/.acme.sh", name+"_ecc", name+".conf")
		_, activeErr := os.Stat(conf)
		if activeErr != nil && !errors.Is(activeErr, os.ErrNotExist) {
			return activeErr
		}
		if activeErr == nil {
			if err := command(acmeBinary, "--remove", "-d", name, "--ecc"); err != nil {
				return fmt.Errorf("取消自动续签失败，未删除证书: %w", err)
			}
		} else if _, err := os.Stat(conf + ".removed"); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("检查 ACME 续签记录失败: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	target := filepath.Join(paths.SSLDir, name)
	if err := m.apply(c, []fileChange{
		{Path: filepath.Join(target, "fullchain.pem"), Mode: 0644, Delete: true},
		{Path: filepath.Join(target, "key.pem"), Mode: 0600, Delete: true},
	}); err != nil {
		return fmt.Errorf("删除失败；自动续签可能已取消: %w", err)
	}
	if err := os.RemoveAll(stage); err != nil {
		return fmt.Errorf("HTTPS 已关闭，但删除暂存证书失败: %w", err)
	}
	return nil
}
