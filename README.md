# xui-remote

在 Telegram 上改 3x-ui 的路由 —— 并且知道改完之后规则**真的生效了**。

纯 REST 客户端：不碰服务器文件系统，不需要 git 或 Docker，只要一个 admin API token。

```
你:  🔀 路由 → 14 → 改出站 → hk-dmit
bot: ✅ 已暂存：规则 14 出站 tw-gcp → hk-dmit
     还未生效，到「⚙️ 配置」提交。

你:  ⚙️ 配置 → 提交
bot: ✅ 已提交，Xray 已重启
     ⚠️ 验证 0/1 通过

     ❌ ext:custom.dat:CRYPTO-HK
         取样 bluefin.io → tw-gcp，期望 hk-dmit
         可能被第 15 条规则遮蔽

     [调整第 14 条的位置] [回滚这次改动]
```

---

## 快速开始

```bash
go build -o xui-remote .
cp config.example.yaml config.yaml
```

填三个值：

| 字段 | 从哪来 |
|---|---|
| `panel.base_url` | `x-ui settings` 看 port 和 webBasePath，拼成 `http://127.0.0.1:<port><basePath>panel/api/` |
| `panel.api_token` | 面板 → 设置 → 安全 → API 令牌 → 新建（只填名称，scope 默认 admin）。**明文只显示一次** |
| `telegram.token` + `allowed_chat_ids` | @BotFather `/newbot`；chat id 可用 @userinfobot 查 |

然后：

```bash
./xui-remote -check      # 只验证面板连通性
./xui-remote -selftest   # 跑一遍所有读路径（只读）
./xui-remote             # 启动
```

Telegram 里对 bot 发 `/start`。

> `base_url` 里 `panel` 可能出现两次：`webBasePath` 是 `/panel/`、API 路由组是
> `/panel/api`，拼起来就是 `/panel/panel/api/`。

---

## 怎么用

入口是输入框上方的常驻键盘，每个画面都有返回。

| 菜单 | 能做什么 |
|---|---|
| 🔀 **路由** | 规则列表（分页 / 筛选 / 发序号选中）、新增、改出站、改匹配值、调序、删除 |
| 📡 **出站** | 出站列表、健康检查（实测各节点延迟）、订阅管理、删除 |
| 🚪 **入站** | 列表、启停 |
| 🧪 **路由测试** | 域名或 IP → 命中哪个出站，可切换用户身份 |
| ⚙️ **配置** | 暂存区、提交、丢弃、配置快照、回滚 |
| 👥 **客户端** | 列表、搜索、订阅链接、二维码、流量与到期 |
| 📊 **状态** | Xray 状态、错误输出、重启 |

也可以**直接发内容**，bot 会列出候选动作让你选：域名/IP → 查路由，邮箱 → 看客户端，
订阅链接 → 加为出站订阅。

### 典型流程：让一个域名改走香港

```
1. 发送 newdex.io        → 点「查当前路由」，看当前走哪
2. 🔀 路由 → + 新增规则   → 选出站 → 填域名 → 选插入位置
3. ⚙️ 配置 → 查看 diff    → 提交（一次重启）
4. 提交后自动验证，不通过会指出可能遮蔽它的规则，并给回滚按钮
```

### 常用斜杠命令

```
/rules  /outbounds  /inbounds  /test <域名>  /client <标识>
/diff   /apply      /rollback  /status       /health
```

---

## 它在替你防什么

改路由比看上去危险，这些是工具挡在前面的东西。

**改动先暂存，一次提交。** 面板的保存是整份配置替换并重启 Xray，一处一提交等于一处断
一次全部连接。编辑先进暂存区，`⚙️ 配置` 里看完整 diff 再一次落地。

**提交前自动快照，可回滚。** 存的是从面板读回来的**线上**配置 —— 中途有人在网页面板改
过，快照也是真实的样子。回滚是整份写回，界面会说明它一并撤销哪些改动。

**并发冲突会拦下。** 你开始编辑之后有人动过面板，提交时报冲突并给出「覆盖 / 丢弃重做」，
不会默默盖掉。

**提交后验证规则真的生效。** 写进配置 ≠ 规则生效：一条更靠前的规则覆盖同类流量，新规则
永远不触发且没有征兆。提交后会取样实测（分类取一个真实成员），不通过就报出来。

> 这是唯一能验证的时机 —— `routeTest` 打的是运行中的 core，没法对未生效的配置做 dry-run。

**顺序防护。** 一套 3x-ui 配置开头是 api 路由、私有地址封锁、协议封锁，这几条的顺序是
**承重的**：api 入站监听 127.0.0.1，它的流量匹配 `geoip:private`，所以 api 规则排到封锁
规则后面，面板的 gRPC 就瞎了 —— 流量统计、在线用户、连路由测试本身全部失效。

新规则插不进这段，往上移也会被拒绝并说明原因。推荐插入位落在第一个 catch-all 之前。

**用户约束感知。** 带 `user` 的规则对不指定身份的探测永远不可达。路由测试会警告并让你
选身份，验证断言也会带上规则自己的 user 约束。

**视图会说明自己是草稿还是线上。** 规则列表渲染暂存草稿，路由测试打运行中的 core ——
列表标 ✏️，测试结果注明它反映线上配置。

---

## 部署到服务器

```bash
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /tmp/xr .
scp /tmp/xr $HOST:/tmp/xui-remote
scp xui-remote.service $HOST:/tmp/
scp config.yaml $HOST:/tmp/xui-remote-config.yaml

ssh $HOST 'sudo bash -s' <<'EOF'
id xui-remote >/dev/null 2>&1 || useradd --system --no-create-home --shell /usr/sbin/nologin xui-remote
install -m 0755 /tmp/xui-remote /usr/local/bin/xui-remote
install -d -m 0750 -o root -g xui-remote /etc/xui-remote
install -m 0640 -o root -g xui-remote /tmp/xui-remote-config.yaml /etc/xui-remote/config.yaml
install -m 0644 /tmp/xui-remote.service /etc/systemd/system/
systemctl daemon-reload && systemctl enable --now xui-remote
EOF
```

服务不以 root 运行；暂存区和快照放在 systemd 的 `StateDirectory`（`/var/lib/xui-remote`），
重启后保留。

```bash
systemctl status xui-remote
journalctl -u xui-remote -f
```

**跑在面板本机时 `base_url` 必须用 `127.0.0.1`** —— 机器通常解析不了自己的 MagicDNS 名字。
跑在同一 tailnet 的其他机器上，用 `tailscale serve` 的地址（真证书，无需跳过校验）。

---

## 排查

| 现象 | 原因 |
|---|---|
| `认证失败：检查 api_token` | token 无效、被禁用，或不是 admin scope |
| 响应不是 JSON，带一段 HTML | `base_url` 路径不对，请求打到了面板前端 |
| 规则列表和路由测试对不上 | 有改动还在暂存区没提交，看列表里的 ✏️ 标记 |
| 验证报「未匹配任何规则」 | 规则可能带 `user` 约束；工具会带上，手动测试要自己选身份 |
| 按钮点了说「已过期」 | bot 重启过。按钮里的短 token 存在内存，重启即失效 |
| `409 Conflict: terminated by other getUpdates` | 同一个 bot token 跑了两个实例 |

面板升级后先跑 `-selftest`：单元测试挡不住面板版本改序列化，而那是生产环境第一个暴露的
问题。

### 已知的面板版本差异（v3.7.0 实测，代码已兼容）

- `/xray/` 的 `obj` 是**双重编码**的 JSON 字符串，其他接口是普通对象
- `/clients/links/:email` **不查 hosts 表**，地址从请求 Host 推导、端口用入站内部端口 ——
  所以分享链接改用 `/clients/subLinks/:subId`（走订阅引擎，认 hosts 表）
- geodata 条目是 `{total, items:[{kind,value}]}`，不是带前缀的字符串列表
- `geosite:` / `geoip:` 不带文件名，而面板拒绝空文件名

---

## 安全

`allowed_chat_ids` 之外的消息**直接丢弃、不回应**，空名单拒绝启动。**名单里每个人都有面板
的完整控制权**，没有分级。

配置文件里同时有面板 admin token 和 bot token，权限 `0640`。别和面板自带 tgbot 共用一个
bot token —— 两个进程对同一 token 做 long polling 会互相抢 update。

真正的单点是你的 Telegram 账号，建议开两步验证。

---

## 开发

```bash
go test ./...        # 单元测试
./xui-remote -selftest   # 对着真面板跑所有读路径
```

加模块：实现 `bot.Module`，按需再实现 `Commander`（斜杠命令）、`TextMatcher`（裸文本候选
动作）、`Prompter` / `PayloadPrompter`（追问），然后在 `main.go` 里 `bot.Register`。

几条约定：

- **回调数据是 `<模块名>:<动作>:<参数>`**，注册时重名直接 panic。上限 64 字节，长值用
  `c.Stash` 换短 token，**token 必须带类别标记** —— 否则查不到时会被误当成字面值。
- **裸文本不被任何功能独占。** 发进来的内容列出候选动作让用户选。
- **每个画面都要有出口**：`bot.Back` / `bot.BackTo` 回上一层，`bot.Home()` 回主菜单。
  常驻键盘不算出口，它跳到模块顶层而不是你来的地方。
- **按钮文字要自我说明**：带上它是什么（`端口 80`、`协议 TCP`），当前值用 `•` 标出。裸
  数字只在它本身就是标识时用。
- **不要静默降级。** 编辑失败吞掉、查不到当字面值、两个视图各说各话不吭声 —— 这三类都在
  实际使用中变成了看起来像数据错乱的 bug。

### 刻意没做

**改入站端口 / 新建入站。** 前者要提交完整 inbound 对象，而 `list/slim` 会剥掉 settings、
`model.Inbound` 的序列化与反序列化形状又不一致，往返有损坏入站的风险；后者几十个字段，在
Telegram 上配不出来。面板 UI 做这两件事是安全的。

**客户端增删改、告警推送、数据库备份。** 面板自带 tgbot 已覆盖，这个工具专注它做不了的
配置面。
