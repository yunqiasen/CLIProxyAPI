# CPA 插件说明目录

> 这是 CPA 插件商店的中文说明表。功能后面同时写了使用场景，避免只看名称不知道什么时候该用。
>
> **快照日期：2026-08-08**
> **插件商店来源：** 管理 API `GET /v0/management/plugin-store`
> **本机运行状态来源：** 管理 API `GET /v0/management/plugins`

## 当前本机状态

| 状态 | 插件 |
|---|---|
| 运行中 | `antigravity-coding-filter`、`codex-429-autoban`、`codex-invite` |
| 已发现但未注册生效 | `jshandler`、`privacyfilter` |
| 插件商店中暂未安装 | 其余插件 |

> “已发现但未注册生效”表示文件或配置已被 CPA 发现，但当前没有进入有效运行状态；不等同于插件商店的 `installed` 字段。

## 全部插件

| # | 插件 ID / 名称 | 功能 | 使用场景：什么时候用 |
|---:|---|---|---|
| 1 | `jshandler`（JS Handler） | 用 JavaScript 拦截、修改请求、响应和流式内容。 | 需要改请求头、请求体或返回内容，但不想修改 CPA 源码时。 |
| 2 | `codex-invite`（Codex Invite） | 使用指定 Codex OAuth 账号发送邀请邮件。 | 给团队成员批量发送 Codex 邀请时。 |
| 3 | `codex-quota-scheduler`（Codex Quota Scheduler） | 根据额度、重置时间和 Fill First 策略调度 Codex 账号。 | Codex 账号较多，希望优先使用额度充足账号时。 |
| 4 | `keeper`（CPA Usage Keeper） | 在管理中心增加打开 CPA Usage Keeper 的入口。 | 已部署 Usage Keeper，希望从 CPA 面板直接进入时。 |
| 5 | `privacyfilter`（Privacy Filter） | 发往上游前隐藏邮箱、手机号、API Key、Token 等敏感信息。 | 担心提示词、代码或日志里的秘密被上游模型看到时。 |
| 6 | `antigravity-coding-filter`（Antigravity Coding Filter） | 拦截可能触发 Antigravity 风控的编程、逆向类请求。 | 使用 Antigravity 跑编码任务，需要降低账号风控概率时。 |
| 7 | `gemini-cli`（Gemini CLI） | 通过原生插件 ABI 增加 Gemini CLI 上游供应商。 | 想把 Gemini CLI 账号接入 CPA 统一调用时。 |
| 8 | `priority-auto-router`（Priority Auto Router） | 按优先级路由 Claude Code、Codex CLI 模型，并自动回退。 | 配置了多个供应商，需要主线路失败后自动切备用线路时。 |
| 9 | `codex-429-autoban`（Codex 429 Auto Ban） | Codex 返回 429 时自动禁用账号，额度恢复后自动启用。 | 多账号轮询时，不想手动处理额度耗尽账号时。 |
| 10 | `model-fallback-router`（Model Fallback Router） | 主模型因额度、限流、网络或指定 HTTP 状态失败时切备用模型。 | 主模型不稳定，但希望请求自动完成时。 |
| 11 | `cpa-key-policy`（CPA Key Policy） | 管理下游 API Key、模型别名、RPM、每日/每周费用上限。 | 给不同用户发独立 Key，并限制模型权限、速率和预算时。 |
| 12 | `cpa-session-archive`（CPA Session Archive） | 将请求归档为去重会话和 Agent 回合，支持检索与训练数据导出。 | 需要复盘历史对话、排查 Agent 问题或制作训练数据时。 |
| 13 | `embeddings-forward`（Embeddings Rerank Forward） | 转发 OpenAI Embeddings 和 Cohere 风格 Rerank 请求，支持别名和 Key 故障切换。 | 应用需要向量化、语义搜索或重排序接口时。 |
| 14 | `codex-token-usage`（CPA Token Usage） | 展示 Codex 用量、额度、429 状态和供应商成本。 | 需要集中查看 Codex 消耗和成本时。 |
| 15 | `model-mapper`（Model Mapper） | 将客户端模型名映射到 Claude、Chat Completions 或 Responses 上游模型。 | 客户端模型名固定，但实际要请求另一个模型时。 |
| 16 | `codexcont`（CodexCont） | 检测 GPT-5.5 推理流被截断并自动折叠续写。 | GPT-5.5 长推理经常输出不完整时。 |
| 17 | `codexcomp`（CodexComp） | 处理 GPT-5.5 流式推理截断，利用加密推理内容继续执行。 | 需要让复杂任务在推理截断后继续完成时。 |
| 18 | `grok-panel`（Grok Panel） | 提供 Grok 账号用量、订阅等级、健康检查和凭据清理面板。 | 管理多个 Grok 免费、Super 或 Heavy 账号时。 |
| 19 | `usage-statistics`（Usage Statistics） | 将 Token、延迟、TTFT、推理等级、服务等级和失败记录到 SQLite。 | 需要长期统计调用量、性能和失败率时。 |
| 20 | `cap-token-usage-tracker`（CAP Token Usage Tracker） | 按小时汇总模型、供应商、执行器、别名、来源、认证方式等多维用量。 | 想做详细运营报表，分析消耗来源时。 |
| 21 | `grok-inspection`（Grok Inspection） | 后台检查 Grok/xAI 账号权限、额度、登录和健康状态。 | Grok 账号池较大，需要批量体检、分类或批量操作时。 |
| 22 | `opencode-go-pool`（OpenCode Go Pool） | 将 OpenCode Go 订阅账号池化，按额度跨协议调度和故障切换。 | 有多个 OpenCode Go 订阅，需要稳定账号池时。 |
| 23 | `cpa-account-config-manager`（CPA Account Config Manager） | 管理账号配置，支持脱敏查看、导入导出、用量查看和批量编辑。 | 经常迁移、批量修改或整理 CPA 账号配置时。 |
| 24 | `quota-router`（Quota Router） | Claude OAuth 达到可配置的七日额度阈值后自动绕开该账号。 | 需要保护 Claude 主账号周额度，避免提前耗尽时。 |
| 25 | `codex-agent-identity`（Codex Agent Identity） | 导入 Codex Agent Identity JWT/PAT，并用加密旁路保存和同步凭据。 | 使用组织级 Agent Identity 或 PAT，而不是普通 OAuth 时。 |
| 26 | `quota-activation`（Quota Activation） | 手动或自动激活 Codex、Antigravity 的额度重置。 | 额度已经刷新，但需要触发恢复流程时。 |
| 27 | `cpa-quota-api-extension`（CPA Quota API Extension） | 通过鉴权管理 API 导出脱敏、统一格式的额度数据。 | Grafana、监控脚本或外部自动化需要读取 CPA 额度时。 |
| 28 | `anti-model-fallback`（Anti Model Fallback） | 检测上游是否偷偷换模型，不一致时重试，超过预算后报错。 | 必须保证实际处理模型和请求模型完全一致时。 |
| 29 | `opencode-cloak`（OpenCode Cloak） | 给 OpenCode 请求补充 Claude Code 的计费标记和客户端指纹，同时保留原系统提示词。 | 通过 Claude OAuth 使用 OpenCode，担心被识别成第三方客户端时。 |
| 30 | `cpa-apply-patch`（Apply Patch Bridge） | 将 Codex 原生 `apply_patch` 转成 Claude 等后端支持的函数工具，并还原响应事件。 | Codex 客户端连 Claude 等不支持 freeform 工具的模型时。 |
| 31 | `credential-priority`（Credential Priority） | 按最新额度证据自动调整 Antigravity、Codex、xAI 凭据优先级。 | 希望高额度账号优先使用、低额度账号自动靠后时。 |
| 32 | `codex-pat`（Codex PAT） | 校验并导入 Codex Personal Access Token，转换为 CPA 可用凭据。 | 手里是 Codex PAT，不是标准 OAuth 凭据时。 |
| 33 | `cliproxyapi-copilot`（GitHub Copilot Provider） | 通过 GitHub 设备码 OAuth、动态模型发现和 Claude Messages 转换接入 Copilot。 | 已有 GitHub Copilot 订阅，希望通过 CPA 调用时。 |
| 34 | `sub2api-balance`（Sub2API Balance） | 查看 Sub2API 供应商余额、额度、倍率和高峰计费窗口。 | CPA 接了多个 Sub2API 渠道，需要核算余额和成本时。 |
| 35 | `grok2api-egress`（Grok Egress Guard） | 管理 Grok/xAI 出口节点，支持粘性代理、质量探测、隔离和迁移。 | Grok 返回质量受代理线路影响，需要自动切换异常出口时。 |
| 36 | `codex-auto-reset`（Codex Auto Reset） | 在 Reset Bank 额度过期前自动兑换，带幂等重试和三次校验。 | Codex 有即将过期的重置额度，不想手动领取或浪费时。 |
| 37 | `key-model-access`（Key Model Access） | 为下游 API Key 设置模型允许/禁止策略，并提供独立认证。 | 给客户或员工发 Key，只允许调用指定模型时。 |
| 38 | `deepseek-vision`（DeepSeek Vision） | 用视觉模型先解析图片，再把图片信息转换给纯文本 DeepSeek。 | 需要让不支持图片的 DeepSeek 处理截图、照片或图表时。 |
| 39 | `codex-localcompact`（Codex Local Compact） | 为 DeepSeek 和 OpenAI 兼容模型模拟 Codex Remote Compaction V2。 | Codex 长会话接第三方模型，需要压缩上下文、降低 Token 消耗时。 |

## 后续更新方法

插件商店会变化，更新本文件时按下面顺序：

1. 从当前 CPA 管理 API 重新获取插件商店列表：

   ```bash
   curl -sS \
     -H "X-Management-Key: $CPA_MANAGEMENT_KEY" \
     "$CPA_BASE/v0/management/plugin-store" \
     > /tmp/cpa-plugin-store.json
   ```

2. 对比 `id`：
   - 新增插件：补一行功能和使用场景。
   - 删除插件：从“全部插件”表移除，保留更新记录即可。
   - 描述、版本或标签变化：更新对应行。
3. 再从 `/v0/management/plugins` 更新“当前本机状态”。
4. 修改“快照日期”。
5. 更新后运行：

   ```bash
   git diff --check
   ```

> 下次你说“更新 CPA 插件说明表”，就按这两个管理 API 重新核对并更新本文件，不改管理台 UI。
