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

## 关键文件

```text
drivers/all.go
drivers/guangyapan/api.go
drivers/guangyapan/driver.go
drivers/guangyapan/driver_test.go
drivers/guangyapan/meta.go
drivers/guangyapan/types.go
drivers/guangyapan/upload.go
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

## 本地验证

运行 GuangYaPan 驱动单元测试：

```bash
cd /root/openlist-guangyapan-src
/root/.local/go/bin/go test -count=1 ./drivers/guangyapan
```

构建二进制：

```bash
cd /root/openlist-guangyapan-src
/root/.local/go/bin/go build -tags=jsoniter -o openlist-guangyapan .
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
- 操作真实网盘文件前，建议先用测试目录和小文件验证移动、复制、重命名、删除、上传。
- 不要把构建产物 `openlist-guangyapan`、`.prev` 备份文件、运行数据目录提交进 git。
- GuangYaPan 接口字段可能会变化，如遇到写操作失败，优先检查 `drivers/guangyapan/api.go` 和 `drivers/guangyapan/upload.go` 中的接口路径与请求体。

## 最近提交

```text
f057739 feat(guangyapan): add OpenList driver
```

该提交包含 GuangYaPan 驱动源码、注册入口和单元测试。
