# FreeTier 2API 二开加载

**提交后自动测试、编译、加载与回滚脚本已实现。** 本机是否启用监听、实际加载到哪个提交，以状态命令为准。

```bash
cd /home/div/1_Project_dir/AI/CPA/freetier2api
./scripts/dev-plugin status
```

| 位置 | 作用 |
| --- | --- |
| `/home/div/1_Project_dir/AI/CPA/freetier2api` | 插件源码、测试、发布工具；在 `freetier2api_fork` 开发 |
| `/home/div/1_Project_dir/AI/CPA/CLIProxyAPI` | CPA 宿主，平时改插件不需要改这里 |
| `/home/div/1_Project_dir/AI/CPA/CLIProxyAPI/logs/plugins/linux/amd64` | 自动安装编译产物，不在这里写插件源码 |
| `/home/div/1_Project_dir/AI/CPA/Cli-Proxy-API-Management-Center` | CPA 管理台；插件自带页面另在插件源码中 |

```text
二开分支：改代码 → 测试/审查 → 本地提交
    ↓ 监听服务发现干净的新提交
固定快照全测/编译 → 安装独立 .so → CPA 选中版本
    ├─ 实际注册路径、文件、页面、模型与凭证一致：记录成功
    └─ 失败：验证回滚，记录原因，停止重复尝试
```

保存未提交、脏工作区、`main` 不发布；不依赖 push。插件页面刷新后体验新版。

| 操作 | 命令（在插件目录） |
| --- | --- |
| 查看状态 | `./scripts/dev-plugin status` |
| 暂停 / 恢复后续发布 | `./scripts/dev-plugin pause` / `./scripts/dev-plugin resume` |
| 手动发布 / 排查后重试 | `./scripts/dev-plugin publish` / `./scripts/dev-plugin publish --retry` |
| 回滚 | `./scripts/dev-plugin rollback` |

**两个限制：**回滚使用新文件副本，避开旧库回调复用问题；每个 CPA 进程原生映射最多八份，达到门槛暂停发布，安排维护重载后继续。不会为了插件提交自动重启 CPA。

自动验收不发模型请求、不签到、不刷新供应商模型；凭证、备注、别名、启停设置作为一致性检查。
有意改变模型清单或状态格式时，先暂停并明确兼容方案。

安装、服务日志、失败处理、分支同步及详细验收统一维护在插件仓库：
[插件二开与自动加载手册](/home/div/1_Project_dir/AI/CPA/freetier2api/docs/local-delivery.md)。
英文接口说明：[Plugin fork maintenance](/home/div/1_Project_dir/AI/CPA/docs/plugin-fork.md)。
