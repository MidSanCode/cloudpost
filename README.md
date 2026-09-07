# Cloudpost 邮件中心

一个用 Go 编写的单二进制自托管邮件中心：内置 SMTP / POP3 / IMAP 服务、多账号管理、网页版安装向导、从第三方邮箱（Gmail、QQ 邮箱、企业邮箱等）通过 POP3/IMAP 拉取邮件并支持过滤规则、SQLite 存储、Material Design 3 Web 界面。

## 功能

- **协议服务**：入站 SMTP（AUTH PLAIN/LOGIN、SMTPUTF8）、POP3（USER/PASS、UIDL、TOP）、IMAP4rev1（SELECT/ FETCH/ SEARCH/ STORE/ COPY/ MOVE/ APPEND / IDLE 兼容）
- **多账号**：本地邮箱账号（带密码，可用于所有协议登录）+ 远程拉取账号（POP3/IMAP 客户端）
- **网页安装向导**：首次访问引导设置管理员密码、邮件域名、服务端口、SMTP 中继
- **收信**：发往主域名的邮件直接入站投递到对应邮箱的 INBOX
- **发信**：网页写邮件；同域即时投递，外部域走 SMTP 队列（直投对方 MX，或配置中继），失败自动退避重试
- **远程拉取**：可配置 IMAP（SSL/STARTTLS/无）或 POP3（SSL/STLS/无），按间隔轮询，增量拉取（IMAP UID / POP3 UIDL 断点），可选服务器端保留邮件
- **过滤规则**：按发件人/收件人/主题/内容/任意字段匹配（包含/等于/前后缀/正则），动作：移动到文件夹、**移动到邮箱（跨账号：自动把匹配邮件分发进另一个本地邮箱的收件箱，适合远程拉取邮件分流）**、标记已读、加星标、移入回收站、丢弃；顺序匹配，首条命中生效
- **Webmail**：文件夹、会话列表、HTML 安全渲染（iframe sandbox）、搜索、星标、移动、删除、下载原始 .eml
- **写邮件增强**：附件（多文件，单文件 ≤20MB / 合计 ≤35MB，自动构造 MIME multipart）、邮件签名（账号编辑中设置，支持 `{{date}}` `{{time}}` `{{datetime}}` `{{from}}` `{{address}}` `{{subject}}` 动态变量，发送时渲染）、延时发送（写邮件时填「延时分钟」，到期自动投递；延时期内可在 Webmail 顶栏「定时箱」一键撤回）
- **存储**：SQLite（WAL）存元数据 + 磁盘存原始邮件 blob
- **UI**：Material Design 3 令牌与组件（动态色板、深色模式、导航栏、对话框、Snackbar），图标使用 Font Awesome 6（CDN 引入，离线环境可将 `webui/index.html` 中的 CDN 换成本地包）
- **DNS 指引**：安装向导与控制台总览页自动生成当前域名所需的 DNS 记录清单（A / MX / SPF / PTR / DMARC），含记录值与用途说明
- **强制重置**：设置页「危险区」可将项目恢复到未安装状态（两步确认：输入 `RESET` → 输入 `删除数据`）；删除全部账号/邮件/过滤规则/队列/配置，协议服务保持运行
- **管理员密码找回**：忘记密码时可用命令行强制重置（见下文「重置管理员密码」）

## 快速开始

```
# 构建（需要 Go 1.25+）
go build -o cloudpost.exe ./cmd/cloudpost

# 运行（默认端口 Web 8080 / SMTP 2525 / POP3 1110 / IMAP 1143）
.\cloudpost.exe -data .\cloudpost-data

# 浏览器打开 http://127.0.0.1:8080 进入安装向导
```

自定义监听地址：`-web 0.0.0.0:8080 -smtp 0.0.0.0:25 -pop3 0.0.0.0:110 -imap 0.0.0.0:143`。
数据目录可用环境变量 `CLOUDPOST_DATA` 指定。

### 更改端口（三种方式）

1. **安装向导**：第 3 步填写的端口在完成安装时即时生效（协议服务热重绑，无需重启）
2. **设置页（运行中热改）**：控制台 → 设置 → 服务端口 → 「应用端口」。SMTP/POP3/IMAP 立即在运行中的进程上重绑到新端口，Web 端口修改后自动切换到新地址（页面会提示跳转）。端口被占用时保留原端口并在响应中报错，配置不会写坏
3. **命令行**：`-web/-smtp/-pop3/-imap` 指定监听地址；未显式指定时，重启后自动使用配置中保存的端口

端口优先级：命令行显式指定 > 配置文件（向导/设置页写入）> 内置默认值。
所有端口同时持久化到 `config.json`，重启后继续使用。

## 使用流程

1. **安装向导**：设置管理员密码 → 填写邮件域名（如 `mail.example.com`）→ 端口（可选 SMTP 中继）
2. **创建邮箱**：控制台 → 账号 → 「本地邮箱」，填写地址（`alice@example.com`）和密码
3. **收信**：把域名的 MX/ A 记录指到本机后，其他邮件服务器即可通过 25 端口投递（测试环境用 2525）；也可以从网页端写邮件
4. **客户端连接**：Thunderbird/Foxmail/Outlook
   - SMTP：`服务器:2525`（AUTH = 完整邮箱地址 + 密码）
   - POP3：`服务器:1110`　IMAP：`服务器:1143`
5. **拉取第三方邮箱**：账号 → 「远程拉取账号」，选 IMAP/POP3，填服务器/账号/密码，可选择把拉到的邮件投递到某个本地邮箱（随后过滤规则对其生效）
6. **过滤规则**：控制台 → 过滤规则 → 选择账号 → 新建规则

## 出站发送

- 未配置中继：直接查询收件域 MX 记录投递（生产环境请确保反向 DNS / SPF 配置正确）
- 配置中继：所有外部邮件经中继服务器发送（设置 → SMTP 中继）
- 发送失败自动指数退避重试（最多 8 次），队列可在控制台查看/手动重试
- **发送记录**：网页端写邮件与 SMTP 客户端（Thunderbird 等）发送的邮件都会自动存入发件账号的「Sent」文件夹

## DKIM 签名

设置 → DKIM 签名：输入 Selector → 生成 2048 位 RSA 密钥 → 把页面显示的 DNS TXT 记录（`<selector>._domainkey.<域名>`）添加到你的 DNS 服务商 → 打开「启用签名」并保存。之后所有发自本域名的外发邮件自动附加 DKIM 签名，配合 SPF 显著降低被判垃圾邮件的概率。私钥只保存在本机配置文件中，更换密钥后记得同步更新 DNS 记录。

## REST API 自动化（访问令牌）

为本地邮箱生成 **API 访问令牌**，外部脚本无需邮箱密码即可调用该邮箱的 REST API（令牌格式 `cpat_…`，数据库只存 SHA-256 哈希，明文仅在创建时显示一次）。

**生成/管理**：控制台 → 账号 → 本地邮箱行 → 「令牌」；或直接调 API：

```bash
# 生成（需管理员登录）
curl -X POST https://host/api/accounts/1/tokens \
  -H "Cookie: cp_admin=<session>" -d '{"label":"ci-script"}'
# → {"token":{...},"secret":"cpat_xxx","usage":"Authorization: Bearer <secret> on /api/mail/* endpoints"}

# 列出 / 吊销
curl https://host/api/accounts/1/tokens    -H "Cookie: cp_admin=<session>"
curl -X DELETE https://host/api/accounts/1/tokens/<tokenID> -H "Cookie: cp_admin=<session>"
```

**用令牌调数据面 API**（管理员与邮箱会话均可管理的全部 `/api/mail/*` 接口）：

```bash
TOK="cpat_xxx"; BASE=https://host
curl -H "Authorization: Bearer $TOK" $BASE/api/mail/me            # 邮箱身份
curl -H "Authorization: Bearer $TOK" $BASE/api/mail/folders       # 文件夹列表
curl -H "Authorization: Bearer $TOK" "$BASE/api/mail/folders/1/messages?limit=50"
curl -H "Authorization: Bearer $TOK" "$BASE/api/mail/folders/1/messages/<id>"   # 读信
curl -H "Authorization: Bearer $TOK" -X POST $BASE/api/mail/compose \
  -H "Content-Type: application/json" -d '{"to":"someone@example.com","subject":"hi","body":"..."}'
```

令牌权限与邮箱登录完全相同（读写该邮箱、可发信），吊销立即生效；`last_used_at` 可用于审计是否仍在使用。远程拉取账号不提供令牌。

## 架构

```
cmd/cloudpost/       入口：装配所有服务
internal/state/      安装配置 + 管理会话
internal/db/         SQLite 打开与迁移
internal/mailstore/  账号/文件夹/消息/过滤器存储，MIME 解析，blob 存储
internal/smtpd/      入站 SMTP（go-smtp 后端 + 出站钩子）
internal/pop3d/      POP3 服务（手写协议）
internal/imapd/      IMAP4rev1 服务（go-imap v2 imapserver）
internal/sender/     出站队列：MX 直投 / 中继 + 重试
internal/fetch/      远程拉取：IMAP（imapclient）/ POP3 客户端 + 调度
internal/filter/     过滤引擎
internal/tlsutil/    自签证书生成
internal/web/        REST API + 静态 UI 托管
webui/               Material Design 3 前端（原生 JS，无构建步骤）
smoke/               端到端冒烟测试客户端
```

## 重置管理员密码

忘记管理员密码时，在服务器本机执行（需要对数据目录的文件访问权限——这本身就是自托管实例的信任边界）：

```
# 方式一：直接传参（注意 shell 历史会记录明文）
.\cloudpost.exe -data .\cloudpost-data -reset-admin "新密码至少6位"

# 方式二：从 stdin 读入（推荐，不留命令行历史）
.\cloudpost.exe -data .\cloudpost-data -reset-admin -
```

行为说明：

- 重置后服务照常启动，用新密码登录即可
- 所有已发的管理员会话立即失效，旧 Cookie 无法继续使用
- 未安装过的实例会拒绝执行（提示先跑安装向导）
- 密码长度限制 6–72 字节（bcrypt 上限）
- 恢复登录后请从服务启动命令中移除 `-reset-admin` 参数（systemd 用户记得 `daemon-reload` 前先改 unit 文件）

## 注意事项

- 默认端口为非特权端口（2525/1110/1143），生产环境可用防火墙端口转发或以管理员身份运行监听 25/110/143
- 未启用 TLS 加密协议监听（内网/测试场景）；公网部署建议前置反向代理或配置证书
- 出站直投依赖本机 25 端口出站未被运营商封锁；云服务器通常封锁 25 出站，此时请配置中继
- `smoke/` 目录为开发用端到端测试客户端，生产部署可删除
