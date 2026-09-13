# xui-remote

通过 Telegram 远程配置 3x-ui 面板。纯 REST API 客户端，不碰服务器文件系统。

## 能做什么

| 模块 | 功能 |
|---|---|
| 🔀 路由 | 列出 / 新增 / 删除 / 调序路由规则，catch-all 遮蔽标注 |
| 📡 出站 | 出站列表与删除、健康检查、订阅管理、balancer 临时覆盖 |
| 🚪 入站 | 入站列表与启停 |
| 🧪 路由测试 | 域名或 IP → 命中哪个出站 |
| ⚙️ 配置 | 暂存区审阅、提交、丢弃、配置快照与回滚 |
| 👥 客户端 | 搜索、订阅链接、单条链接、二维码、流量 |
| 📊 状态 | 面板与 Xray 状态、错误输出、重启 Xray |

## 设计要点

**改动先暂存，再一次提交。** `/panel/api/xray/update` 是整份配置替换并重启
Xray，所以每改一处就提交会反复断连。所有编辑进暂存区，`⚙️ 配置 → 提交` 一次落地。

**提交前自动存快照。** 快照存的是提交前从面板读回的<b>线上</b>配置，不是 bot
记忆的状态 —— 中途有人在网页面板改过也能如实回滚。

**并发冲突会拦下。** 暂存区记录开始编辑时的线上配置，提交前比对；网页面板在这期间
改过就报冲突而不是覆盖。

**快照只覆盖配置模板。** 入站、客户端、出站订阅存在数据库里，由面板在生成配置时合并，
不在快照范围内，回滚不会动它们。

**提交后自动验证，而不是假定成功。** 见上。

**裸文本不被任何功能独占。** 发一个域名进来，bot 列出候选动作让你选，而不是直接执行。
新功能只是往这个菜单里加一个按钮。

**域名一律显式加前缀。** Xray 路由规则里的裸字符串是子串匹配，而 geosite 源文件里
是后缀匹配。bot 给裸域名自动补 `domain:`，避免把规则悄悄放宽。

## 安装

```bash
go build -o xui-remote .
cp config.example.yaml config.yaml   # 填 token
./xui-remote -check                  # 只验证面板连通性
./xui-remote -selftest               # 跑一遍所有读路径（见下）
./xui-remote
```

## 自检

`-selftest` 用真配置把每条读路径跑一遍：读配置模板并解析规则与出站、routeTest、
分类取样、订阅链接，最后检查生成的链接**没有指向客户端到不了的地址**（loopback、
tailnet、私有网段）。只读，不写任何东西。

单元测试挡不住面板版本改序列化 —— 这恰恰是生产环境第一个暴露的问题。部署到新面板、
或面板升级之后，先跑这个。

部署到服务器：

```bash
GOOS=linux GOARCH=amd64 go build -o xui-remote .
scp xui-remote $HOST:/usr/local/bin/
scp xui-remote.service $HOST:/etc/systemd/system/
ssh $HOST "mkdir -p /etc/xui-remote /var/lib/xui-remote"
scp config.yaml $HOST:/etc/xui-remote/
ssh $HOST "systemctl enable --now xui-remote"
```

## 配置

见 `config.example.yaml`。两个容易踩的地方：

**`base_url` 里 panel 出现两次。** 面板的 `webBasePath` 是 `/panel/`，API 路由组
是 `/panel/api`，拼起来就是 `/panel/panel/api/`。用 `x-ui settings` 查自己的
`webBasePath`。

**`api_token` 必须是 admin 权限。** 面板 → 设置 → 安全 → API 令牌 → 新建令牌。
创建弹窗只要一个名称，scope 留空即默认 admin。<b>明文只显示一次</b>，存的是
SHA-256 哈希，关掉弹窗就找不回来了。

`allowed_chat_ids` 不能为空，否则拒绝启动 —— admin token 等于面板完整控制权，
没有白名单意味着任何找到这个 bot 的人都能改配置。

## 刻意没做

**改入站端口。** `/inbounds/update/:id` 要提交完整的 inbound 对象，而 `list/slim`
会剥掉 settings、`model.Inbound` 的序列化与反序列化形状又不一致，GET→POST 往返有
丢字段进而损坏入站的风险。面板 UI 做这件事是安全的，就留给它。

**新建入站。** 协议、传输、TLS/REALITY 几十个字段，在 Telegram 上配不出来。

**客户端增删改、告警推送、数据库备份。** 面板自带的 tgbot 已经覆盖，这个 bot 专注
它做不了的配置面。

**提交后自动验证。** 新增规则时记下断言，提交完自动跑一遍 `routeTest`：分类会取一个
真实成员做样本，字面域名直接测。不通过就报出来，并指出最可能遮蔽它的那条更早的规则。

这是唯一能验证的时机 —— `routeTest` 打的是运行中的 core，没法对未生效的配置做 dry-run。
而分类之间的重叠（比如新域名已经落在 `category-entertainment` 里）算不出来，只能靠
落地后实测发现。关键字和正则匹配无法取样，会标为跳过而不是假装通过。

模块通过 `bot.Register` 注册，加功能不需要动框架：实现 `Module` 接口，按需再实现
`Commander`（斜杠命令）、`TextMatcher`（裸文本候选动作）、`Prompter`（追问）。
回调数据统一是 `<模块名>:<动作>:<参数>`，命名空间由框架保证不冲突。
