# Issue #1 验收记录

范围：[Telegram 配置可靠性与交互改进](https://github.com/sleepingF0x/xui-remote/issues/1)。审查基线为 `4683d3aa`。

测试从 Telegram 消息或按钮进入真实 Bot 分发，经过真实模块、事务管理和临时状态目录；只在 Telegram 与面板 HTTP API 边界使用本地模拟。回归用例位于 `internal/integration/bot_test.go`。

| 验收项 | 已验证的结果 | 主要回归用例 |
|---|---|---|
| 并发配置操作 | 并发编辑及提交期间的新编辑保留所有已确认改动，或明确拒绝；回滚不覆盖待处理草稿 | `TestConcurrentEditsKeepEveryAcknowledgedChange`、`TestEditDuringApplyIsNeverSilentlyLost`、`TestPanelConflictAndRollbackPreservePendingDraft` |
| 命令上下文 | 命令进入后的按钮、提问、取消和确认均可继续执行 | `TestSlashCommandButtonsAndReplies`、`TestOutboundAndInboundCommandsAndOldDelete` |
| 过期引用 | 旧删除、移动、改目标、匹配值输入、提交、丢弃及报告按钮拒绝执行，并提供刷新入口 | `TestOldConfirmationDoesNotDeleteAnotherRule`、`TestStaleEditsAndConfirmationsHaveNoEffect`、`TestVerificationTracksFinalPositionsAndSkipsDeletedRules` |
| 最终验证 | A→B→C 只验证 C；删除对象不再验证；移动后使用最终位置；不能取样或缺少均衡器路径时明确跳过 | `TestFinalPreviewAndVerificationIgnoreIntermediateTarget`、`TestVerificationTracksFinalPositionsAndSkipsDeletedRules`、`TestFinalBalancerVerificationAndUnprobeableConstraints` |
| 目标类型 | 普通出站与均衡器分别写入对应字段；显示文字相似也不会混淆身份 | `TestNewRuleTargetsReachPanelAndNewThenDeletedIsNetZero`、`TestTargetIdentityDoesNotDependOnDisplaySuffix` |
| 长内容 | 长日志与完整 JSON 可重组；中文、表情及转义字符完整，每片 HTML 合法，末片按钮可继续使用 | `TestLongLogIsCompleteUTF8AndValidHTML`、`TestRuleFilteredPageAndLongJSON` |
| 路由列表 | 每页 8 条描述性按钮，可翻页；列表不占用下一条域名输入 | `TestRuleListLeavesTextAvailableAndReturnsToFilter`、`TestRuleFilteredPageAndLongJSON` |
| 路由详情 | 显示匹配约束、目标、顺序和草稿状态；完整 JSON 单独打开并可返回 | `TestRuleFilteredPageAndLongJSON` |
| 最终变化预览 | 重复编辑合并；相同邻接规则不产生虚假移动；改回原值、新增后删除不更新面板 | `TestRepeatedRuleEditsCollapseToOneFinalChange`、`TestEditingIdenticalNeighbourDoesNotInventMoves`、`TestNetZeroNeverWritesOrRestarts` |
| 导航与取消 | 路由和客户端保留页码、筛选；清除直接生效；菜单切换使迟到的旧提问失效 | `TestClientReturnsToFilteredPageAndClearsSearch`、`TestRuleFilteredPageAndLongJSON`、`TestMenuSwitchCannotResurrectOldPrompt` |
| 订阅快捷输入 | 长 URL 只输入一次；动作按钮不暴露完整 URL；预览不创建，确认后才创建 | `TestSubscriptionURLIsEnteredOnceAndExplicitlyConfirmed` |

旧草稿缺少版本和身份元数据时仍可编辑、提交，历史检查记录不再干扰最终验证：`TestLegacyDraftWithoutMetadataStillEditsAndApplies`。快照仍保留完整模板；快速连续保存也具有不同 ID。

## 反向审查

交付前重点质疑并验证了以下问题：

1. **有锁是否就不会丢改动？** 用请求屏障制造并发时序，检查保存后的草稿与每条成功反馈一致，而不只依赖竞态检测。
2. **旧按钮是否仍能把同一序号当成同一对象？** 按钮携带配置版本，写入事务内再次检查；变化后只能刷新。
3. **最终状态是否会被重复编辑和相同规则误导？** 暂存元数据保留规则的基线对应关系；多字段编辑与相同邻接规则的回归均通过。
4. **显示文字和实际目标身份是否混淆？** 出站名称含 `(balancer)` 的复现先失败；改用目标字段和名称比较后通过。
5. **取消是否能阻止尚未完成的旧请求重新提问？** 请求屏障复现菜单切换后的迟到提示；按聊天交互代次保护后，普通输入继续进入动作识别。

独立 Standards 审查发现的目标身份问题已关闭；独立 Spec 审查发现的差异归并、相同规则及迟到提示问题均已复测关闭。两个方向无剩余可行动发现。

## 检查结果与边界

以下检查全部通过：

- `go test -race ./...`
- `go vet ./...`
- `go build ./...`
- `gofmt -l internal` 无输出
- `git diff --check` 无输出

尚未连接真实 Telegram 或真实面板，也未进行手机端截图验收；不将模拟服务的结果表述为线上端到端验证。测试没有向生产面板发送写入。面板 API 未提供跨客户端原子更新，因此仍保留外部配置变化提醒与明确覆盖确认，不承诺跨客户端原子性。
