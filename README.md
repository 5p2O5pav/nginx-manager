# Nginx Manager

面向 Debian / Ubuntu（systemd、apt）的本机 Nginx 反向代理管理工具。开发需求见 `开发文档V1.md`。

## 构建与启动

需要 Go 1.24 或更新版本；Debian 12 自带的软件仓库 Go 版本可能不足，请先从 [Go 官方下载页](https://go.dev/dl/)安装适用于本机架构的版本，并用 `go version` 确认。构建时需要网络下载 Go 模块。Linux 上执行：

```sh
go mod download
CGO_ENABLED=0 go build -trimpath -o nm ./cmd/nm
sudo install -m 0755 nm /usr/local/bin/nm
sudo nm
```

首次运行会在未安装 Nginx 时询问是否安装，然后提示接管和备份现有 Nginx 配置；两处均可按回车同意。ACME.sh 需要预先以 root 身份安装到 `/root/.acme.sh/acme.sh`，并启用其续签定时任务。程序接管后，原配置备份位于 `/etc/nm/nginx-original-backup`。

例如，参考 [ACME.sh 官方安装说明](https://github.com/acmesh-official/acme.sh#installation)以 root 安装：

```sh
sudo apt-get update
sudo apt-get install -y git curl openssl ca-certificates cron
sudo systemctl enable --now cron
sudo -i
git clone https://github.com/acmesh-official/acme.sh.git /root/acme.sh-source
cd /root/acme.sh-source
./acme.sh --install
exit
```

安装后通过 `sudo /root/.acme.sh/acme.sh --version` 和 `sudo crontab -l` 检查程序及续签任务。若安装失败，请根据 ACME.sh 的输出处理；本项目不会自动安装它。

添加域名、路径规则并签发证书后才能通过 HTTPS 访问。签发前应将域名 DNS 指向服务器并确保公网 80 端口可访问。后端可以填写 `127.0.0.1:30001`；不填路径默认 `/`，不填协议默认 HTTP。

运行 `nm` 后，首页显示 Nginx 在线状态。菜单按域名管理、转发规则管理、证书管理和配置管理分组。添加域名成功后可直接按回车签发证书，或输入 `n` 稍后签发。转发规则按域名分组显示并缩进列出；选择某条规则后可进入删除确认，再按一次回车即可删除并立即生效。通过菜单添加或删除配置后会立即校验并应用；配置管理中的“应用配置”用于手动重新应用的情况。

## 命令行示例

```sh
sudo nm add example.com
sudo nm route add example.com / 127.0.0.1:30001 --websocket
sudo nm cert issue example.com
sudo nm list
sudo nm cert status example.com
sudo nm validate
sudo nm backup
sudo nm backups
sudo nm restore 备份文件名.json --yes
```

规则通过 `nm route list 域名` 查看，使用 `nm route edit 域名 路径 后端` 修改同一路径规则，`nm route delete 域名 路径` 删除。添加或修改时可选择 `--strip`、`--websocket`；HTTPS 后端还可选择 `--server-name=名称`、`--ca=CA文件绝对路径` 或 `--insecure`。最后一项会关闭上游证书验证。

域名可使用 `nm domain enable 域名`、`nm domain disable 域名`、`nm domain rename 旧域名 新域名 --yes` 和 `nm domain delete 域名 --yes` 管理。修改域名会删除旧证书及续签记录，新域名必须重新签发。证书可用 `nm cert delete 域名 --yes` 删除。

`nm settings max-upload-mb 数值` 设置最大上传大小，0 表示不限制；`nm settings timeout-seconds 数值` 设置代理超时，0 使用默认值 3600 秒。

## 开发状态

本项目尚未完成实际 Linux/Nginx/ACME.sh 环境的端到端验证。建议使用独立测试 VPS：先验证初始化和 HTTP 验证路径，再签发测试域名并测试代理、上传下载、WebSocket、续签和回滚。证书签发使用 Let's Encrypt 生产环境，应注意签发频率限制。

已知限制：ACME.sh 自行运行的定时续签不持有本项目的域名锁；不要在续签过程中并行执行证书签发、删除、域名启停或重命名。恢复 JSON 备份只恢复配置，不会恢复已删除的证书或自动调整 ACME.sh 的续签记录；恢复后请检查证书与续签状态。修改域名当前只提供删除旧证书的方式。
