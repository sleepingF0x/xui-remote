# xui-remote

在 Telegram 上改 3x-ui 的路由 —— 并且知道改完之后规则**真的生效了**。

<img src="docs/screenshots/status-and-menu.png" width="380" alt="常驻键盘与运行状态">

改动先进暂存区，一批只重启一次 Xray。提交后自动验证规则是否真的生效：

```
⚙️ 配置 → 提交

✅ 已提交，Xray 已重启
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

填四个值：

| 字段 | 从哪来 |
|---|---|
| `panel.base_url` | `x-ui settings` 看 port 和 webBasePath，拼成 `http://127.0.0.1:<port><basePath>panel/api/` |
| `panel.api_token` | 面板 → 设置 → 安全 → API 令牌 → 新建（只填名称，scope 默认 admin）。**明文只显示一次** |
| `telegram.token` | @BotFather 发 `/newbot` 拿到的那串 |
| `telegram.allowed_chat_ids` | 你的 chat id，可用 @userinfobot 查 |

然后：

```bash
./xui-remote -check      # 只验证面板连通性
./xui-remote -selftest   # 跑一遍所有读路径，只读不写
./xui-remote             # 启动
```

Telegram 里对 bot 发 `/start`。

> **两个容易配错的地方**
>
> `base_url` 里 `panel` 可能出现两次：`webBasePath` 是 `/panel/`、API 路由组是
> `/panel/api`，拼起来就是 `/panel/panel/api/`。
>
> `allowed_chat_ids` 不能留空，否则拒绝启动。名单外的消息直接丢弃、不回应；**名单里的
> 每个人都有面板的完整控制权**，没有分级。

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

也可以**直接发内容**，bot 会列出候选动作让你选：

| 你发什么 | 能做什么 |
|---|---|
| 域名或 IP | 查当前路由 |
| 客户端标识（邮箱） | 看该客户端 |
| 订阅链接 | 加为出站订阅 |

### 改动要先提交才生效

所有编辑先进**暂存区**，`⚙️ 配置 → 提交` 才写进面板。这样一批改动只重启一次 Xray，而不是
改一处断一次连接。

提交前能看完整 diff，也能整批丢弃。**提交前会自动存一份快照**，随时回滚。

> 规则列表显示的是暂存草稿，标 ✏️ 的就是还没生效的。路由测试打的是运行中的配置 ——
> 两边对不上时，看看是不是有改动没提交。

### 典型流程：让一个域名改走香港

```
1. 发送 newdex.io        → 点「查当前路由」，看当前走哪
2. 🔀 路由 → + 新增规则   → 选出站 → 填域名 → 选插入位置
3. ⚙️ 配置 → 看 diff      → 提交（一次重启）
4. 自动验证，不通过会指出可能挡住它的规则，并给回滚按钮
```

### 关于插入位置

路由是**首个匹配生效**，所以位置决定一切。

- 给的「推荐位置」落在第一个 catch-all 规则之前 —— 那条之后的规则不会生效
- 开头几条基础设施规则（api 路由、私有地址封锁）**插不进去也移不上去**，那几条的顺序动了
  面板自己会瞎掉
- 想自己定位置就点「指定序号…」，会先把当前顺序列出来再让你填

### 关于用户约束

规则如果带 `user` 限定，**不指定身份测是永远匹配不上的**。路由测试里点「指定用户…」选一个
客户端再测。存在这类规则时 bot 会提醒你。

### 常用斜杠命令

```
/rules  /outbounds  /inbounds  /test <域名>  /client <标识>
/diff   /apply      /rollback  /status       /health
```

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

```bash
systemctl status xui-remote
journalctl -u xui-remote -f
```

**跑在面板本机时 `base_url` 必须用 `127.0.0.1`** —— 机器通常解析不了自己的 tailnet 域名。

### 更新版本

```bash
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /tmp/xr .
scp /tmp/xr $HOST:/tmp/xui-remote
ssh $HOST 'sudo install -m 0755 /tmp/xui-remote /usr/local/bin/xui-remote \
  && sudo systemctl restart xui-remote && rm /tmp/xui-remote'
```

---

## 排查

| 现象 | 原因 |
|---|---|
| `认证失败：检查 api_token` | token 无效、被禁用，或不是 admin scope |
| 响应不是 JSON，带一段 HTML | `base_url` 路径不对，请求打到了面板前端 |
| 规则列表和路由测试对不上 | 有改动还在暂存区没提交，看列表里的 ✏️ 标记 |
| 测试报「未匹配任何规则」 | 规则可能带 `user` 约束，点「指定用户…」选个身份再测 |
| 按钮点了说「已过期」 | bot 重启过，按钮里的短 token 存在内存，重启即失效 |
| bot 不回消息 | 你的 chat id 不在 `allowed_chat_ids` 里；看日志会记下被拒的 id |
| `409 Conflict: terminated by other getUpdates` | 同一个 bot token 跑了两个实例 |
| 提交后订阅节点没生效 | 出站订阅的改动要等 Xray 重启，用 📊 状态里的重启按钮 |

面板升级之后先跑一次 `-selftest`，它会把所有读路径过一遍。

---

## 这些事请去面板做

| | 为什么 |
|---|---|
| 新建入站、改入站端口 | 字段太多，而且改端口需要完整对象往返，有损坏入站的风险 |
| 新增 / 删除客户端、改流量和到期 | 面板自带的 Telegram bot 已经覆盖 |
| 告警推送、数据库备份 | 同上 |

自带 bot 和这个可以并存，但**别共用一个 bot token** —— 两个进程对同一 token 收消息会互相
抢，消息随机丢。
