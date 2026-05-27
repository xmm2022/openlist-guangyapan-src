# 139 CAS Tools

这些脚本是 139Yun CAS 的离线辅助工具，不是 OpenList 服务运行必需组件。它们会调用 139 PC 端接口，把分享文件临时转存到当前账号目录，读取 SHA256 后生成 OpenList 可用的 `.cas` 文件，再清理临时源文件。

## 工具边界

- `single_share_to_cas_batch.py`：单链接多文件。一次处理一个 139 分享链接中的多个文件，支持断点进度、失败重试、多 worker 和按大小分批。
- `multi_share_to_cas_batch.py`：多链接批量。读取 manifest 中的多个分享任务，逐个调用 `single_share_to_cas_batch.py`，每个分享使用独立进度文件。

不要把运行数据提交进仓库，包括分享列表 JSON、进度 JSON、日志、Cookie、数据库和任务输出目录。

## 单链接多文件

适用场景：一个 139 分享链接里包含很多视频文件，需要批量生成 `.cas`。

```bash
python3 tools/cas139/single_share_to_cas_batch.py \
  --share-id share-xxxx \
  --files-json /path/to/139_share_files.json \
  --owner-general-json /path/to/139_share_general.json \
  --db-path /opt/openlist/data/data.db \
  --workers 3 \
  --order input \
  --max-batch-size 60TiB \
  --max-file-size 500GB \
  --progress /path/to/139_share_cas_progress.json \
  --retry-failed
```

常用参数：

```text
--share-id                 139 分享 ID/linkID；单链接工具每次只处理一个分享
--files-json               分享文件列表 JSON，顶层需要包含 files 数组
--owner-general-json       可选，用于提取 ownerUserId
--db-path                  OpenList data.db 路径，用于读取 139Yun 存储授权
--storage-id               指定 OpenList 139Yun 存储 ID
--mount-path               指定 OpenList 139Yun 挂载路径
--workers                  并发 worker 数；每个 worker 使用独立临时目录
--progress                 断点进度 JSON
--retry-failed             重试 progress.failed 中的文件
--include-non-video        默认只处理视频容器；开启后也处理字幕/元数据等非视频文件
--verify-restore           生成 .cas 后额外探测 SHA256 秒传恢复
--dry-run                  只打印计划，不调用网盘接口
```

## 多链接批量

适用场景：有多个 139 分享链接，每个链接各自有一个文件列表 JSON，需要顺序批量处理。

manifest 支持 JSON：

```json
{
  "shares": [
    {
      "share_id": "share-a",
      "files_json": "/path/to/share-a-files.json",
      "progress": "/path/to/share-a-progress.json"
    },
    {
      "share_id": "share-b",
      "files_json": "/path/to/share-b-files.json",
      "progress": "/path/to/share-b-progress.json"
    }
  ]
}
```

也支持 JSONL，每行一个对象。

运行：

```bash
python3 tools/cas139/multi_share_to_cas_batch.py \
  --manifest /path/to/cas139-shares.json \
  --db-path /opt/openlist/data/data.db \
  --progress-dir /path/to/progress \
  --workers 3 \
  --retry-failed
```

多链接工具只是调度器；具体处理仍由单链接工具完成。某个分享失败时，默认继续处理后续分享；需要失败即停时加 `--stop-on-error`。

## 输出和风险

- 输出 `.cas` 默认写入 139Yun 账号中的 `__openlist_cas_import_out` 目录。
- 临时转存目录默认是 `__openlist_cas_import_tmp`；多 worker 或多链接会自动加后缀隔离。
- 工具会调用分享转存、文件创建、回收站和永久删除接口；先用小分享和测试目录验证。
- 如果已有同名 `.cas`，工具会跳过并记录 `already_exists`。
- 运行中断后优先保留 progress，再使用 `--retry-failed` 恢复失败项。

## 本地自测

```bash
python3 tools/cas139/test_single_share_to_cas_batch.py
python3 tools/cas139/test_multi_share_to_cas_batch.py
python3 -m py_compile tools/cas139/*.py
```
