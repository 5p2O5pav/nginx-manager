//go:build linux

package manager

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"nginx-manager/internal/config"
)

// SetEnabled pauses ACME renewal by using acme.sh's own --remove operation.
// acme.sh preserves the domain configuration as .conf.removed, which is
// restored on re-enabling. Only certificates installed by nm are managed.
func SetEnabled(name string, enabled bool) error {
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
	defer m.Close()
	c, err := m.Read()
	if err != nil {
		return err
	}
	index := -1
	for i, d := range c.Domains {
		if d.Name == name {
			index = i
			break
		}
	}
	if index < 0 {
		return fmt.Errorf("域名不存在: %s", name)
	}
	if c.Domains[index].Enabled == enabled {
		return nil
	}
	stage := filepath.Join(acmeStage, name)
	_, stageErr := os.Stat(stage)
	if stageErr != nil && !errors.Is(stageErr, os.ErrNotExist) {
		return stageErr
	}
	// Issue uses acme.sh's default ECC key type. Explicit --ecc keeps the
	// removal target consistent with its installation and renewal entry.
	conf := filepath.Join("/root/.acme.sh", name+"_ecc", name+".conf")
	removed := conf + ".removed"
	_, activeErr := os.Stat(conf)
	_, removedErr := os.Stat(removed)
	if activeErr != nil && !errors.Is(activeErr, os.ErrNotExist) {
		return activeErr
	}
	if removedErr != nil && !errors.Is(removedErr, os.ErrNotExist) {
		return removedErr
	}
	managed := activeErr == nil || removedErr == nil
	if managed && !enabled {
		if err := command(acmeBinary, "--remove", "-d", name, "--ecc"); err != nil {
			return fmt.Errorf("暂停续签失败，域名未禁用: %w", err)
		}
	}
	if managed && enabled {
		if _, err := os.Stat(conf); err == nil {
			return fmt.Errorf("续签配置已存在，无法安全恢复: %s", conf)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if _, err := os.Stat(removed); err != nil {
			return fmt.Errorf("找不到暂停的续签配置: %w", err)
		}
	}
	c.Domains[index].Enabled = enabled
	if err := m.Apply(c); err != nil {
		if managed && !enabled {
			if restoreErr := os.Rename(removed, conf); restoreErr != nil {
				return fmt.Errorf("应用失败: %v；恢复续签失败: %w", err, restoreErr)
			}
		}
		return err
	}
	if managed && enabled {
		if err := os.Rename(removed, conf); err != nil {
			return fmt.Errorf("域名已启用，但恢复自动续签失败: %w", err)
		}
	}
	return nil
}

// RenameDomain moves routes to a new name after cleaning the old certificate.
// The new name must obtain its own certificate before HTTPS is available.
func RenameDomain(oldName, newName string) error {
	oldName, err := config.DomainName(oldName)
	if err != nil {
		return err
	}
	newName, err = config.DomainName(newName)
	if err != nil {
		return err
	}
	lock, err := lockCertificate(oldName)
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
		if d.Name == oldName {
			found = true
		}
		if d.Name == newName {
			m.Close()
			return fmt.Errorf("域名已存在: %s", newName)
		}
	}
	m.Close()
	if !found {
		return fmt.Errorf("域名不存在: %s", oldName)
	}
	for _, dir := range []string{filepath.Join(paths.SSLDir, oldName), filepath.Join(acmeStage, oldName)} {
		if _, err := os.Stat(dir); err == nil {
			if err := deleteCertificateLocked(oldName); err != nil {
				return err
			}
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	m, err = Open()
	if err != nil {
		return err
	}
	defer m.Close()
	c, err = m.Read()
	if err != nil {
		return err
	}
	for i := range c.Domains {
		if c.Domains[i].Name == oldName {
			c.Domains[i].Name = newName
			if err := m.Apply(c); err != nil {
				return fmt.Errorf("重命名失败，旧证书可能已删除: %w", err)
			}
			return nil
		}
	}
	return fmt.Errorf("原域名已被其他操作删除: %s", oldName)
}

// DeleteDomain removes the certificate first so a registered renewal cannot
// subsequently recreate a deleted site's configuration.
func DeleteDomain(name string) error {
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
		if d.Name == name {
			found = true
			break
		}
	}
	m.Close()
	if !found {
		return fmt.Errorf("域名不存在: %s", name)
	}
	for _, dir := range []string{filepath.Join(paths.SSLDir, name), filepath.Join(acmeStage, name)} {
		if _, err := os.Stat(dir); err == nil {
			if err := deleteCertificateLocked(name); err != nil {
				return err
			}
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	m, err = Open()
	if err != nil {
		return err
	}
	defer m.Close()
	c, err = m.Read()
	if err != nil {
		return err
	}
	for i, d := range c.Domains {
		if d.Name == name {
			c.Domains = append(c.Domains[:i], c.Domains[i+1:]...)
			if err := m.Apply(c); err != nil {
				return fmt.Errorf("域名删除失败，证书可能已删除: %w", err)
			}
			return nil
		}
	}
	return fmt.Errorf("域名已被其他操作删除: %s", name)
}
