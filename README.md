中文 | [English](README_en.md)

<div align="center">

# Cloodsy S3

**轻量级 AWS SDK 兼容 S3 服务器，使用 Go 编写。**

单文件部署，零依赖 — 无需 CGO、无需外部数据库、无需运行时环境。
所有元数据存储在内嵌 SQLite 数据库中。

[![Built by OnaOnbir](https://img.shields.io/badge/Built%20by-OnaOnbir-blue?style=flat-square)](https://onaonbir.com)
[![Go](https://img.shields.io/badge/Go-1.24-00ADD8?style=flat-square&logo=go&logoColor=white)](https://go.dev)
[![Release](https://img.shields.io/github/v/release/onaonbir/Cloodsy-S3?style=flat-square)](https://github.com/onaonbir/Cloodsy-S3/releases/latest)

[官网](https://onaonbir.com) | [下载](https://github.com/onaonbir/Cloodsy-S3/releases/latest) | [文档](#快速开始)

</div>

---

## 功能特性

- **AWS SDK 兼容** — 支持 AWS CLI、boto3、aws-sdk-go、s3cmd、rclone、Terraform 及所有 S3 兼容客户端
- **单文件部署** — 一个可执行文件，零依赖，随处运行
- **独立 Bucket 凭证** — 每个 Bucket 拥有独立的访问密钥对，支持读写或只读权限
- **对象版本控制** — 按 Bucket 启用、暂停或禁用版本控制，完整支持删除标记
- **生命周期规则** — 基于时间和前缀过滤器的自动对象过期清理
- **Webhook 通知** — 对象事件的实时 HTTP 回调，支持 HMAC 签名验证
- **Bucket 配额** — 按 Bucket 设置存储上限，防止磁盘耗尽
- **自定义存储目录** — 按 Bucket 指定存储路径，支持多磁盘部署（SSD 存热数据，HDD 存归档）
- **预签名 URL** — 无需共享凭证即可生成限时下载/上传链接
- **分片上传** — 大文件分片上传，支持分片复制、列表和自动清理过期上传
- **Range 请求** — 通过标准 HTTP Range 头实现部分文件下载
- **条件请求** — 支持 If-Match、If-None-Match、If-Modified-Since、If-Unmodified-Since
- **服务端复制** — 无需重新上传即可在 Bucket 间复制对象，支持 Range 部分复制
- **Admin REST API** — 基于 Session 认证的完整管理 API，支持 GUI/自动化
- **CORS 支持** — 浏览器端 S3 客户端开箱即用
- **TLS 支持** — 可选 HTTPS 配置
- **安全存储** — 文件以 `.cloodsys3ext` 扩展名存储，防止路径遍历和符号链接攻击
- **图片缩放** — 通过查询参数实时缩放图片，内置安全限制
- **上传自动缩放** — 上传大图时自动异步缩放（S3 和 WebDAV 均支持）
- **WebDAV 支持** — 内置 WebDAV 服务器，直接映射 S3 Bucket 进行文件管理
- **跨平台** — 支持 Linux、macOS、Windows 和树莓派

## 快速开始

### 1. 编译

```bash
make build
```

可执行文件生成在 `build/` 目录。无需配置文件即可运行。

### 2. 创建 Bucket 和凭证

```bash
./cloodsys3 bucket create my-bucket
./cloodsys3 credential create my-bucket
```

输出：
```
Bucket:     my-bucket
Access Key: AK7F2B9X4MPLEPHOTO1
Secret Key: wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLE

Warning: Save the secret key now. It will not be shown again.
```

### 3. 启动服务器

```bash
./cloodsys3 serve
```

默认监听端口 `9000`。

### 4. 使用 AWS CLI

```bash
# 配置 profile
aws configure --profile cloodsy
# 输入 Access Key、Secret Key、Region: us-east-1

# 上传
aws --endpoint-url http://localhost:9000 --profile cloodsy \
    s3 cp file.txt s3://my-bucket/file.txt

# 列表
aws --endpoint-url http://localhost:9000 --profile cloodsy \
    s3 ls s3://my-bucket/

# 同步目录
aws --endpoint-url http://localhost:9000 --profile cloodsy \
    s3 sync ./local-dir s3://my-bucket/remote-dir/

# 下载
aws --endpoint-url http://localhost:9000 --profile cloodsy \
    s3 cp s3://my-bucket/file.txt downloaded.txt

# 删除
aws --endpoint-url http://localhost:9000 --profile cloodsy \
    s3 rm s3://my-bucket/file.txt
```

## CLI 命令参考

所有 CLI 命令均可在服务器运行时使用。SQLite WAL 模式支持服务器和 CLI 并发访问。

### 服务器

```bash
./cloodsys3 serve                        # 默认配置启动
./cloodsys3 serve -config config.yaml    # 使用自定义配置启动
```

### Bucket 管理

```bash
./cloodsys3 bucket create <name>                          # 创建 Bucket
./cloodsys3 bucket create <name> --storage-dir=/mnt/ssd   # 指定存储目录创建
./cloodsys3 bucket list                                   # 列出所有 Bucket
./cloodsys3 bucket info <name>                            # 查看详情
./cloodsys3 bucket delete <name>                          # 删除（必须为空）
./cloodsys3 bucket quota <name> 10GB                      # 设置存储上限（KB/MB/GB/TB，0=无限制）
./cloodsys3 bucket storage <name> --dir=/new/path         # 迁移存储到新位置
./cloodsys3 bucket storage <name> --dir=                  # 重置为默认存储
```

### 凭证管理

每个 Bucket 可以有多个访问密钥对。密钥仅授权访问其所属的 Bucket。

```bash
./cloodsys3 credential create <bucket>              # 创建读写密钥对
./cloodsys3 credential create <bucket> --read-only  # 创建只读密钥对
./cloodsys3 credential list <bucket>                # 列出 Bucket 的密钥
./cloodsys3 credential delete <access-key>          # 撤销指定密钥
```

**权限模型：**
- `read-write`（默认）— GET、PUT、DELETE、HEAD、POST — 完全访问
- `read-only` — 仅 GET、HEAD、ListObjects — 写操作返回 `AccessDenied`

### 版本控制

```bash
./cloodsys3 bucket versioning enable <name>    # 启用版本控制
./cloodsys3 bucket versioning suspend <name>   # 暂停版本控制
./cloodsys3 bucket versioning status <name>    # 查看当前状态
```

启用后，每次 PUT 都会创建一个带唯一 ID 的新版本。删除对象会创建删除标记而非移除数据。历史版本可通过版本 ID 访问。

### 生命周期规则

自动过期指定天数后的对象。

```bash
./cloodsys3 bucket lifecycle set <name> --days=30                  # 30 天后过期所有对象
./cloodsys3 bucket lifecycle set <name> --days=7 --prefix=logs/    # 仅过期 logs/ 下的对象
./cloodsys3 bucket lifecycle get <name>                            # 列出规则
./cloodsys3 bucket lifecycle delete <name>                         # 删除所有规则
./cloodsys3 bucket lifecycle delete <name> --prefix=logs/          # 删除指定规则
```

后台清理器按可配置间隔（默认 `1h`）运行，以 100 个为一批删除过期对象。

### 自定义存储目录

默认所有 Bucket 数据存储在全局 `root_dir` 下。可以为每个 Bucket 指定独立的存储目录，使不同 Bucket 分布在不同磁盘上：

```bash
# 热数据放 SSD
./cloodsys3 bucket create hot-data --storage-dir=/mnt/ssd

# 归档数据放 HDD
./cloodsys3 bucket create archives --storage-dir=/mnt/hdd

# 迁移已有 Bucket 到新位置（自动迁移数据）
./cloodsys3 bucket storage my-bucket --dir=/mnt/nvme

# 验证
./cloodsys3 bucket info hot-data
# Storage: /mnt/ssd/hot-data/ (custom)
```

注意事项：
- `--storage-dir` / `--dir` 必须为绝对路径
- 删除 Bucket 会同时删除其存储目录（无论位置）
- 服务器运行期间通过 CLI 修改存储路径后需重启服务器

### Webhook 通知

对象创建或删除时接收 HTTP 回调。

```bash
./cloodsys3 bucket webhook add <name> --url=https://example.com/hook
./cloodsys3 bucket webhook add <name> --url=https://example.com/hook --events=s3:ObjectCreated:* --secret=mysecret
./cloodsys3 bucket webhook list <name>
./cloodsys3 bucket webhook delete <name> --id=<webhook-id>
```

**支持的事件：** `s3:ObjectCreated:Put`、`s3:ObjectCreated:Copy`、`s3:ObjectRemoved:Delete`，或 `*` 表示全部。

设置 secret 后，请求会包含 `X-Cloodsy-Signature` HMAC-SHA256 头用于验证。事件异步投递，3 次重试，指数退避（1s、2s、4s）。负载格式遵循 AWS S3 事件通知格式。

### 管理员

```bash
./cloodsys3 admin create <username>                     # 自动生成密码创建
./cloodsys3 admin create <username> --password=mypass    # 指定密码创建
./cloodsys3 admin list                                  # 列出管理员
./cloodsys3 admin delete <username>                     # 删除管理员
./cloodsys3 admin password <username>                   # 自动生成新密码
./cloodsys3 admin password <username> --password=new    # 指定新密码
```

管理员用于 Admin REST API 认证。密码以 bcrypt 哈希存储。

### 版本信息

```bash
./cloodsys3 version
```

## S3 API 操作

### 对象操作

| 操作 | 方法 | 端点 |
|------|------|------|
| PutObject | PUT | `/<bucket>/<key>` |
| GetObject | GET | `/<bucket>/<key>` |
| HeadObject | HEAD | `/<bucket>/<key>` |
| DeleteObject | DELETE | `/<bucket>/<key>` |
| DeleteObjects | POST | `/<bucket>?delete` |
| CopyObject | PUT | `/<bucket>/<key>` + `X-Amz-Copy-Source` |

### Bucket 操作

| 操作 | 方法 | 端点 |
|------|------|------|
| ListBuckets | GET | `/` |
| CreateBucket | PUT | `/<bucket>` |
| DeleteBucket | DELETE | `/<bucket>` |
| HeadBucket | HEAD | `/<bucket>` |
| GetBucketLocation | GET | `/<bucket>?location` |
| ListObjects | GET | `/<bucket>` |
| ListObjectsV2 | GET | `/<bucket>?list-type=2` |
| ListObjectVersions | GET | `/<bucket>?versions` |

### 版本控制与生命周期

| 操作 | 方法 | 端点 |
|------|------|------|
| GetBucketVersioning | GET | `/<bucket>?versioning` |
| PutBucketVersioning | PUT | `/<bucket>?versioning` |
| GetBucketLifecycle | GET | `/<bucket>?lifecycle` |
| PutBucketLifecycle | PUT | `/<bucket>?lifecycle` |
| DeleteBucketLifecycle | DELETE | `/<bucket>?lifecycle` |

### 分片上传

| 操作 | 方法 | 端点 |
|------|------|------|
| CreateMultipartUpload | POST | `/<bucket>/<key>?uploads` |
| UploadPart | PUT | `/<bucket>/<key>?partNumber=N&uploadId=X` |
| UploadPartCopy | PUT | `/<bucket>/<key>?partNumber=N&uploadId=X` + `X-Amz-Copy-Source` |
| ListParts | GET | `/<bucket>/<key>?uploadId=X` |
| ListMultipartUploads | GET | `/<bucket>?uploads` |
| CompleteMultipartUpload | POST | `/<bucket>/<key>?uploadId=X` |
| AbortMultipartUpload | DELETE | `/<bucket>/<key>?uploadId=X` |

### 通知

| 操作 | 方法 | 端点 |
|------|------|------|
| GetBucketNotification | GET | `/<bucket>?notification` |
| PutBucketNotification | PUT | `/<bucket>?notification` |
| DeleteBucketNotification | DELETE | `/<bucket>?notification` |

### 兼容性桩

以下操作为兼容 Terraform、s3cmd、rclone 等工具而接受，但不持久化数据：

| 操作 | 方法 | 端点 | 行为 |
|------|------|------|------|
| GetBucketAcl | GET | `/<bucket>?acl` | 返回 FULL_CONTROL |
| PutBucketAcl | PUT | `/<bucket>?acl` | 接受但忽略 |
| GetObjectAcl | GET | `/<bucket>/<key>?acl` | 返回 FULL_CONTROL |
| PutObjectAcl | PUT | `/<bucket>/<key>?acl` | 接受但忽略 |
| GetBucketEncryption | GET | `/<bucket>?encryption` | 返回 SSE-S3 (AES256) |
| PutBucketEncryption | PUT | `/<bucket>?encryption` | 接受但忽略 |
| GetBucketTagging | GET | `/<bucket>?tagging` | 返回 NoSuchTagSet |
| PutBucketTagging | PUT | `/<bucket>?tagging` | 接受但忽略 |
| DeleteBucketTagging | DELETE | `/<bucket>?tagging` | 空操作 |
| GetObjectTagging | GET | `/<bucket>/<key>?tagging` | 返回空 TagSet |
| PutObjectTagging | PUT | `/<bucket>/<key>?tagging` | 接受但忽略 |
| DeleteObjectTagging | DELETE | `/<bucket>/<key>?tagging` | 空操作 |
| GetBucketPolicy | GET | `/<bucket>?policy` | 返回 NoSuchBucketPolicy |
| PutBucketPolicy | PUT | `/<bucket>?policy` | 接受但忽略 |
| DeleteBucketPolicy | DELETE | `/<bucket>?policy` | 空操作 |

## 认证

Cloodsy S3 使用 AWS Signature Version 4 (SigV4) 认证，支持基于 Header 的签名和预签名 URL。

- 每个凭证仅作用于单个 Bucket
- `ListBuckets` 仅返回凭证所属的 Bucket
- 每个 Bucket 可创建多个凭证
- 凭证支持 `read-write` 或 `read-only` 权限
- 支持分块上传签名（`STREAMING-AWS4-HMAC-SHA256-PAYLOAD`）
- 时间偏差容忍：5 分钟

### 预签名 URL

生成限时 URL 用于分享，无需暴露凭证：

```bash
# AWS CLI（有效期 1 小时）
aws --endpoint-url http://localhost:9000 --profile cloodsy \
    s3 presign s3://my-bucket/photo.jpg --expires-in 3600
```

```python
# boto3（有效期 1 小时，最长 7 天）
url = s3.generate_presigned_url(
    'get_object',
    Params={'Bucket': 'my-bucket', 'Key': 'photo.jpg'},
    ExpiresIn=3600
)
```

## Admin REST API

Admin API 提供基于 JSON 的管理接口，运行在独立端口。在配置中启用：

```yaml
admin:
  enabled: true
  listen: ":9001"
  cors_origins: ["*"]
```

### 认证

```bash
# 创建管理员
./cloodsys3 admin create myadmin

# API 登录
curl -X POST http://localhost:9001/admin/login \
  -H "Content-Type: application/json" \
  -d '{"username":"myadmin","password":"<password>"}'

# 响应：{"token":"cks_...","expires_in":86400}

# 后续请求使用 token
curl http://localhost:9001/admin/buckets \
  -H "Authorization: Bearer cks_..."
```

会话 24 小时后过期。Token 存储在内存中，服务器重启后清空。

### 端点

| 方法 | 端点 | 说明 |
|------|------|------|
| POST | `/admin/login` | 登录，返回会话 token |
| POST | `/admin/logout` | 登出 |
| GET | `/admin/status` | 服务器状态 |
| GET | `/admin/admins` | 列出管理员 |
| POST | `/admin/admins` | 创建管理员 |
| DELETE | `/admin/admins/{username}` | 删除管理员 |
| PUT | `/admin/admins/{username}/password` | 修改密码 |
| GET | `/admin/buckets` | 列出所有 Bucket 及统计 |
| POST | `/admin/buckets` | 创建 Bucket |
| GET | `/admin/buckets/{name}` | Bucket 详情 |
| DELETE | `/admin/buckets/{name}` | 删除 Bucket |
| PUT | `/admin/buckets/{name}/quota` | 设置配额 |
| PUT | `/admin/buckets/{name}/storage` | 更改存储目录 |
| GET/PUT | `/admin/buckets/{name}/versioning` | 获取/设置版本控制 |
| GET | `/admin/buckets/{name}/credentials` | 列出凭证（含密钥） |
| POST | `/admin/buckets/{name}/credentials` | 创建凭证 |
| DELETE | `/admin/credentials/{accessKey}` | 删除凭证 |
| GET | `/admin/buckets/{name}/lifecycle` | 列出生命周期规则 |
| POST | `/admin/buckets/{name}/lifecycle` | 创建生命周期规则 |
| DELETE | `/admin/buckets/{name}/lifecycle` | 删除生命周期规则 |
| GET | `/admin/buckets/{name}/webhooks` | 列出 Webhook |
| POST | `/admin/buckets/{name}/webhooks` | 创建 Webhook |
| DELETE | `/admin/webhooks/{id}` | 删除 Webhook |
| GET | `/admin/buckets/{name}/objects` | 列出对象（支持 prefix/delimiter） |
| DELETE | `/admin/buckets/{name}/objects/{key}` | 删除对象 |
| POST | `/admin/buckets/{name}/objects/delete-prefix` | 删除文件夹 |

## 图片缩放

### 实时缩放

在任何图片 GET 请求中添加查询参数即可获取缩放后的版本：

```
GET /<bucket>/<key>?w=800&h=600&q=80&m=fit
```

| 参数 | 说明 | 默认值 |
|------|------|--------|
| `w` | 目标宽度（像素） | — |
| `h` | 目标高度（像素） | — |
| `q` | JPEG 质量（1-100） | 80 |
| `m` | 缩放模式：`fit`（默认）、`fill`、`crop` | `fit` |

**缩放模式：**
- `fit` — 等比例缩小到 w×h 边界框内，保持宽高比（不放大）
- `fill` — 等比例缩放填满 w×h，居中裁剪溢出部分
- `crop` — 从中心裁剪到精确的 w×h

**示例：**
```bash
# 适应 800x600 边界框（保持宽高比）
curl http://localhost:9000/my-bucket/photo.jpg?w=800&h=600

# 仅指定宽度，高度自动计算
curl http://localhost:9000/my-bucket/photo.jpg?w=1920

# 裁剪为 200x200 缩略图
curl http://localhost:9000/my-bucket/photo.jpg?w=200&h=200&m=crop

# 低质量预览
curl http://localhost:9000/my-bucket/photo.jpg?w=400&q=50
```

**安全限制**（通过 `image_resize` 配置）：
- 输出尺寸上限为 `max_width` × `max_height`（默认 4096×4096）
- 放大倍数上限为 `max_upscale` × 原始尺寸（默认 2 倍）
- 并发缩放数上限为 `max_concurrent`（默认 4）

### 上传自动缩放

启用后，通过 S3 PutObject 或 WebDAV 上传的大图会自动在后台异步缩放：

```yaml
image_resize:
  auto_resize_enabled: true     # 启用自动缩放
  auto_resize_min_size: "5MB"   # 仅缩放大于 5MB 的图片
  auto_resize_target_w: 1920    # 目标最大宽度
  auto_resize_target_h: 0       # 0 = 不限制高度
  auto_resize_target_size: "2MB" # 目标文件大小（0=不限制）
  auto_resize_quality: 85       # JPEG 初始质量（二分法会自动降低）
```

**工作流程：**
1. 通过 S3 API 或 WebDAV 上传文件
2. 如果文件是图片且超过 `auto_resize_min_size`，加入缩放队列
3. 后台工作协程（最多 `max_concurrent` 个）异步处理队列
4. **先按尺寸缩放**（如果宽/高超限），**再按目标文件大小压缩**
5. 缩放完成后替换原文件
6. 如果缩放后反而更大（如 PNG 无损格式），保留原文件

**目标文件大小压缩（二分法）：**

当设置了 `auto_resize_target_size`（如 `"2MB"`）时，系统会自动降低 JPEG 质量直到文件大小达标：
- 先用 `auto_resize_quality`（默认 85）编码，如果文件大小已满足目标则直接返回
- 如果超标，使用**二分法**在 [10, 85] 之间搜索最优质量（最多 10 次迭代）
- 最终取满足目标大小的**最高质量**，在文件大小和画质之间取得最佳平衡
- 这是微信/WhatsApp/TinyPNG 等主流图片压缩服务的通用做法
- PNG/GIF 等无损格式会自动转为 JPEG 再压缩

**示例：** 900×7715 的长图（3.4MB），`target_w=1920, target_size=2MB`：
- 宽度 900 < 1920，不需要尺寸缩放
- 但文件 3.4MB > 2MB，触发二分法质量压缩
- 系统自动从质量 85 开始降低，找到满足 ≤2MB 的最高质量

**核心行为：**
- **只缩小不放大** — 750×1920 的图片在 `target_w=1920` 时不会被缩放（宽度 750 已 ≤ 1920）
- **目标文件大小优先** — 即使尺寸不超限，文件大小超标也会压缩
- **非阻塞** — 上传立即返回，缩放在后台进行
- **队列溢出** — 缩放队列满时丢弃任务（上传永远不会被阻塞）
- **Content-Type 检测** — WebDAV 上传的 `application/octet-stream` 会根据文件扩展名自动推断

**缩放示例（target_w=1920, target_h=0）：**

| 原始尺寸 | 结果 | 原因 |
|----------|------|------|
| 4000×3000 | 1920×1440 | 宽度超限，按宽度等比例缩小 |
| 750×1920 | 不缩放 | 宽度 750 < 1920，不限制高度 |
| 3000×5000 | 1920×3200 | 宽度超限，按宽度等比例缩小 |

**缩放示例（target_w=1920, target_h=1920）：**

| 原始尺寸 | 结果 | 原因 |
|----------|------|------|
| 4000×3000 | 1920×1440 | 宽度超限，Fit 到 1920×1920 边界框 |
| 750×1920 | 不缩放 | 宽高都在边界框内 |
| 3000×5000 | 1152×1920 | 高度超限，Fit 到 1920×1920 边界框 |

## WebDAV

Cloodsy S3 内置 WebDAV 服务器，直接映射到 S3 Bucket。支持使用任何 WebDAV 客户端（Windows 资源管理器、macOS Finder、rclone、Cyberduck 等）管理文件。

```yaml
webdav:
  enabled: true
  listen: ":9002"
  prefix: "/"
```

**连接 WebDAV 客户端：**
- URL：`http://localhost:9002/`
- 认证：使用任意 S3 Bucket 凭证（Access Key = 用户名，Secret Key = 密码）
- 每个 Bucket 显示为文件夹

**支持的操作：**
- PROPFIND — 列出文件和文件夹
- GET — 下载文件
- PUT — 上传文件（启用自动缩放时触发）
- MKCOL — 创建文件夹
- DELETE — 删除文件和文件夹
- COPY / MOVE — 复制和移动文件

**注意事项：**
- WebDAV 上传走 S3 存储后端
- S3 API 和 WebDAV 上传均支持自动缩放
- 文件 Content-Type 根据扩展名自动推断（`.jpg` → `image/jpeg` 等）

## 配置

配置文件可选。服务器使用合理的默认值运行。自定义配置请传入 YAML 文件：

```bash
./cloodsys3 serve -config config.yaml
```

```yaml
server:
  listen: ":9000"
  region: "us-east-1"
  tls:
    enabled: false
    cert_file: ""
    key_file: ""

database:
  path: "./.cloodsys3/cloodsys3.db"
  busy_timeout: 5000          # 写锁等待时间（毫秒）
  cache_size: 64000           # 页面缓存大小（KB）
  mmap_size: 134217728        # 内存映射 I/O（字节，128MB）
  max_readers: 4              # 并行读连接数

storage:
  root_dir: "./.cloodsys3/data"
  multipart_max_age: "24h"    # 未完成分片上传自动清理时间
  lifecycle_interval: "1h"    # 生命周期规则检查间隔

logging:
  level: "info"               # debug, info, warn, error
  format: "text"              # text 或 json

admin:
  enabled: false              # 启用 Admin REST API
  listen: ":9001"             # Admin API 端口（与 S3 分离）
  cors_origins:               # CORS 允许的来源
    - "*"

webdav:
  enabled: false              # 启用 WebDAV 服务器
  listen: ":9002"             # WebDAV 端口（与 S3/Admin 分离）
  prefix: "/"                 # WebDAV URL 前缀

image_resize:
  max_width: 4096             # 输出图片最大宽度（像素），防止内存爆炸
  max_height: 4096            # 输出图片最大高度（像素）
  max_upscale: 2              # 最大放大倍数（相对原图）
  max_concurrent: 4           # 最大并发缩放数
  auto_resize_enabled: false    # 上传自动缩放（设为 true 启用）
  auto_resize_min_size: "5MB"   # 触发自动缩放的最小文件大小（支持 KB/MB/GB）
  auto_resize_target_w: 1920    # 目标最大宽度（像素），只缩小不放大
  auto_resize_target_h: 0       # 目标最大高度（0=不限制高度，设为 1920 则宽高都不超过 1920）
  auto_resize_target_size: "0"  # 目标文件大小（支持 KB/MB/GB，0=不限制）
  auto_resize_quality: 85       # JPEG 初始质量（1-100），二分法会自动降低
```

## 安装

### 快速安装（Linux/macOS）

```bash
curl -fsSL https://raw.githubusercontent.com/onaonbir/Cloodsy-S3/main/install.sh | bash
```

自动检测操作系统和架构，下载最新版本并安装到 `/usr/local/bin/`。

### 手动下载

从 [GitHub Releases](https://github.com/onaonbir/Cloodsy-S3/releases/latest) 下载：

| 平台 | 文件 |
|------|------|
| Linux x64 | `cloodsys3-linux-amd64.tar.gz` |
| Linux ARM64（树莓派） | `cloodsys3-linux-arm64.tar.gz` |
| Linux ARMv7 | `cloodsys3-linux-armv7.tar.gz` |
| Windows x64 | `cloodsys3-windows-amd64.zip` |
| macOS Apple Silicon | `cloodsys3-darwin-arm64.tar.gz` |
| macOS Intel | `cloodsys3-darwin-amd64.tar.gz` |

## 更新

```bash
# 检查更新
./cloodsys3 update --check

# 更新到最新版本（自动检测平台）
./cloodsys3 update
```

`update` 命令从 GitHub 下载最新版本并替换当前可执行文件。更新后需重启服务器。

服务器启动时也会检查更新，如果有新版本会记录警告日志。

## 部署

只需部署可执行文件。所有运行时数据自动创建：

```bash
make build
scp build/cloodsys3 server:/opt/cloodsys3/
```

在服务器上：
```bash
cd /opt/cloodsys3
./cloodsys3 bucket create my-bucket
./cloodsys3 credential create my-bucket
./cloodsys3 admin create myadmin          # 可选：用于 Admin API
./cloodsys3 serve
```

运行时目录结构：
```
/opt/cloodsys3/
├── cloodsys3                  # 可执行文件
├── config.yaml                # 可选配置
└── .cloodsys3/                # 运行时数据（自动创建）
    ├── cloodsys3.db           # SQLite 数据库
    ├── cloodsys3.db-wal       # WAL 文件
    ├── cloodsys3.db-shm       # 共享内存
    └── data/                  # 对象存储（默认）
        └── my-bucket/
            ├── photo.jpg.cloodsys3ext
            └── docs/
                └── report.pdf.cloodsys3ext

# 使用 --storage-dir 的 Bucket 使用自定义位置：
/mnt/ssd/
└── hot-bucket/
    └── data.bin.cloodsys3ext
```

### Windows

```powershell
# 从 Linux 交叉编译
make build-windows

# 在 Windows 上
cd C:\CloodsyS3
.\cloodsys3.exe bucket create my-bucket -config config.yaml
.\cloodsys3.exe credential create my-bucket -config config.yaml
.\cloodsys3.exe serve -config config.yaml
```

### 作为服务运行（systemd）

```bash
# 创建系统用户
sudo useradd -r -s /bin/false cloodsys3
sudo mkdir -p /opt/cloodsys3
sudo cp cloodsys3 config.yaml /opt/cloodsys3/
sudo chown -R cloodsys3:cloodsys3 /opt/cloodsys3

# 安装服务
sudo cp cloodsys3.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable cloodsys3
sudo systemctl start cloodsys3

# 查看状态
sudo systemctl status cloodsys3

# 查看日志
sudo journalctl -u cloodsys3 -f
```

服务故障时自动重启，延迟 5 秒。

### 安全存储

上传的文件以 `.cloodsys3ext` 扩展名存储在磁盘上，防止意外执行。额外安全措施：

- **路径遍历防护** — 所有路径均验证不超出基础目录
- **符号链接攻击防护** — 文件使用 `O_NOFOLLOW` 标志打开
- **原子写入** — 临时文件 + 重命名模式防止部分读取
- **安全头** — `X-Content-Type-Options: nosniff`、`X-Frame-Options: DENY`

## 安全

- **S3 API** 运行在独立端口（默认 `:9000`）— 对 S3 客户端暴露
- **Admin API** 运行在独立端口（默认 `:9001`）— 通过防火墙限制为可信网络
- **凭证** — 按 Bucket 作用域，支持只读权限
- **管理员密码** — 以 bcrypt 哈希存储，永不明文
- **会话 Token** — 24 小时有效期，仅存内存，重启后清空
- **CORS** — 可配置允许的来源

## SDK 示例

### Python (boto3)

```python
import boto3

s3 = boto3.client(
    "s3",
```

### Go (aws-sdk-go-v2)

```go
cfg, _ := awsconfig.LoadDefaultConfig(context.TODO(),
    awsconfig.WithRegion("us-east-1"),
    awsconfig.WithCredentialsProvider(
        credentials.NewStaticCredentialsProvider("AKXX...", "YYYY...", ""),
    ),
)

client := s3sdk.NewFromConfig(cfg, func(o *s3sdk.Options) {
    o.BaseEndpoint = aws.String("http://localhost:9000")
    o.UsePathStyle = true
})

client.PutObject(context.TODO(), &s3sdk.PutObjectInput{
    Bucket: aws.String("my-bucket"),
    Key:    aws.String("test.txt"),
    Body:   strings.NewReader("hello world"),
})
```

### JavaScript (AWS SDK v3)

```javascript
import { S3Client, PutObjectCommand } from "@aws-sdk/client-s3";

const client = new S3Client({
  endpoint: "http://localhost:9000",
  region: "us-east-1",
  credentials: {
    accessKeyId: "AKXX...",
    secretAccessKey: "YYYY...",
  },
  forcePathStyle: true,
});

await client.send(new PutObjectCommand({
  Bucket: "my-bucket",
  Key: "test.txt",
  Body: "hello world",
}));
```

## 编译目标

```bash
make build            # 当前平台
make build-linux      # Linux x86_64
make build-windows    # Windows x86_64
make build-mac        # macOS Apple Silicon (ARM64)
make build-mac-intel  # macOS Intel (x86_64)
make build-pi         # 树莓派 3/4/5 (ARM64)
make build-armv7      # 树莓派 2 / 旧 ARM (ARMv7)
make build-all        # Linux (amd64 + arm64 + armv7)
make clean            # 删除 build 目录
make version          # 打印当前版本
```

## 许可证

Cloodsy S3 基于 [Cloodsy S3 Community License 1.0](LICENSE) 开源。

可自由用于个人、内部、教育和自托管用途。
商业转售、SaaS 服务和竞争性托管服务需要单独的商业许可证。

联系：[trademark@onaonbir.com](mailto:trademark@onaonbir.com)

---

<div align="center">

**Cloodsy S3** 由 **[OnaOnbir](https://onaonbir.com)** 构建和维护

[onaonbir.com](https://onaonbir.com)

</div>
