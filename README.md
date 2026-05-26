# OpenList GuangYaPan 二开版

这是基于 OpenList 二开的光鸭云盘测试版本，目标是让 OpenList 可以挂载 GuangYaPan，并向 Emby 等播放器提供可播放的文件链接。

当前源码仓库位置：

```bash
/root/openlist-guangyapan-src
```

当前测试实例使用端口 `5247`。用户原有的 OpenList `5246` 实例未纳入本仓库管理，也不应被本项目的测试命令影响。

## 已完成能力

- 新增 `GuangYaPan` 驱动并注册到 OpenList。
- 支持目录列表，修复光鸭目录被识别成文件的问题。
- 支持获取文件播放/下载链接，可用于 Emby 拉取播放地址。
- 支持新建文件夹。
- 支持删除文件或文件夹。
- 支持移动文件或文件夹。
- 支持复制文件或文件夹。
- 支持重命名文件或文件夹。
- 支持上传文件：
  - 计算文件 MD5。
  - 调用光鸭秒传检查接口。
  - 获取光鸭资源中心 OSS 临时凭证。
  - 通过 Aliyun OSS SDK 上传文件。
- 合入 CAS 轻量占位文件能力：
  - `139Yun` 支持 SHA256 型 `.cas`，适用于 `personal_new` 类型。
  - `189CloudPC` 支持 MD5 / SliceMD5 型 `.cas`。
  - 可在上传真实文件后生成同名 `.cas`，并按配置删除源文件。
  - 访问 `.cas` 播放时临时恢复真实文件，获取真实播放链接后清理临时目录。
  - 支持通过 `/d/*.cas` 或 `/p/*.cas` 进入真实文件预览/Range 播放流程。

## 关键文件

```text
drivers/all.go
drivers/guangyapan/api.go
drivers/guangyapan/driver.go
drivers/guangyapan/driver_test.go
drivers/guangyapan/meta.go
drivers/guangyapan/types.go
drivers/guangyapan/upload.go
internal/casmeta/casmeta.go
internal/driver/cas.go
drivers/139/cas.go
drivers/189pc/cas_payload.go
drivers/189pc/cas_restore.go
drivers/189pc/cas_preview.go
```

## 驱动配置

在 OpenList 管理后台添加存储时，驱动选择：

```text
GuangYaPan
```

需要填写的主要字段：

```text
access_token
refresh_token
client_id
device_id
page_size
order_by
sort_type
```

`client_id` 默认值为：

```text
aMe-8VSlkrbQXpUR
```

`root_folder_id` 可留空，表示光鸭根目录。

## CAS 配置

### 139Yun

139 CAS 当前适用于驱动类型：

```text
personal_new
```

可用配置字段：

```text
generate_cas
delete_source
restore_source_from_cas
cas_ext_allowlist
cas_download_restore
```

注意：139 的当前实现依赖 SHA256 秒传恢复。云端只有 MD5、没有 SHA256 的文件不能生成可恢复的 139 `.cas`。

### 189CloudPC

189CloudPC CAS 使用 MD5 / SliceMD5 秒传恢复，可用配置字段：

```text
generate_cas
delete_source
restore_source_from_cas
delete_cas_after_restore
auto_restore_existing_cas
auto_restore_existing_cas_paths
cas_ext_allowlist
cas_download_restore
```

`cas_ext_allowlist` 留空表示允许所有扩展名；建议用于 Emby 的目录只放媒体扩展名，例如：

```text
mp4,mkv,avi,mov,ts,m2ts
```

用于 STRM/Emby 的常见方式是只保留 `.cas` 或由 `.cas` 生成的可访问地址，等用户播放时再临时恢复真实文件并获取播放链接。

## 从公开仓库安装

公开仓库地址：

```bash
https://github.com/xmm2022/openlist-guangyapan-src
```

按源码方式安装：

```bash
git clone https://github.com/xmm2022/openlist-guangyapan-src.git openlist-guangyapan-src
cd openlist-guangyapan-src
go build -tags=jsoniter -o openlist-guangyapan .
./openlist-guangyapan server --data ./data
```

## 一键部署到 9876 端口

脚本会从 GitHub Release 下载已经编译好的 Linux 二进制，不需要在服务器上安装 Go。

脚本默认安装到：

```text
/opt/openlist-guangyapan
```

默认 systemd 服务名：

```text
openlist-guangyapan.service
```

默认 HTTP 端口：

```text
9876
```

一键部署：

```bash
curl -fsSL https://raw.githubusercontent.com/xmm2022/openlist-guangyapan-src/main/scripts/deploy-9876.sh | sudo bash
```

如果需要覆盖端口或安装路径：

```bash
curl -fsSL https://raw.githubusercontent.com/xmm2022/openlist-guangyapan-src/main/scripts/deploy-9876.sh | \
  sudo env HTTP_PORT=9876 INSTALL_DIR=/opt/openlist-guangyapan bash
```

支持的默认二进制架构：

```text
linux-amd64
linux-arm64
```

部署后访问：

```text
http://127.0.0.1:9876
```

查看服务状态：

```bash
systemctl status openlist-guangyapan --no-pager
journalctl -u openlist-guangyapan -f
```

## 本地验证

运行 GuangYaPan 驱动单元测试：

```bash
cd /root/openlist-guangyapan-src
go test -count=1 ./drivers/guangyapan
go test -count=1 ./internal/casmeta ./drivers/139 ./drivers/189pc ./server/handles
```

构建二进制：

```bash
cd /root/openlist-guangyapan-src
go build -tags=jsoniter -o openlist-guangyapan .
```

## 5247 测试实例

当前测试服务名：

```bash
openlist-guangyapan-test.service
```

测试数据目录：

```bash
/root/openlist-guangyapan-test-data
```

启动方式示例：

```bash
systemd-run \
  --unit=openlist-guangyapan-test \
  --description='OpenList GuangYaPan test instance' \
  --property=WorkingDirectory=/root/openlist-guangyapan-src \
  --property=Restart=on-failure \
  --property=RestartSec=3 \
  --property=StandardOutput=append:/root/openlist-guangyapan-test-data/server.log \
  --property=StandardError=append:/root/openlist-guangyapan-test-data/server.log \
  /root/openlist-guangyapan-src/openlist-guangyapan server \
  --data /root/openlist-guangyapan-test-data \
  --log-std
```

检查 5247 是否正常监听：

```bash
ss -ltnp | rg '(:5246|:5247)'
```

预期结果中应同时看到：

```text
:5247  openlist-guangyapan
:5246  openlist
```

其中 `5246` 是用户原有实例，不属于本项目测试实例。

## 注意事项

- 本仓库是 OpenList 的 GuangYaPan 二开测试源码，不是官方 OpenList 发布版。
- CAS 会调用网盘秒传/恢复和删除接口；开启 `delete_source` 前请先用测试目录验证。
- 操作真实网盘文件前，建议先用测试目录和小文件验证移动、复制、重命名、删除、上传。
- 不要把构建产物 `openlist-guangyapan`、`.prev` 备份文件、运行数据目录提交进 git。
- GuangYaPan 接口字段可能会变化，如遇到写操作失败，优先检查 `drivers/guangyapan/api.go` 和 `drivers/guangyapan/upload.go` 中的接口路径与请求体。

## 关键提交

```text
f057739 feat(guangyapan): add OpenList driver
b080322 docs: update guangyapan readme
745c6aa feat: add CAS support for 139 and 189pc
5dbe894 docs: document CAS usage
```

这些提交包含 GuangYaPan 驱动源码、注册入口、单元测试、139/189CloudPC CAS 能力和基本使用说明。部署脚本见 `scripts/deploy-9876.sh`。
