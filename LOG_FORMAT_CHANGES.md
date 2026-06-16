# 日志格式修改说明

## 修改时间
2026-06-16 18:57

## 修改文件
- `./internal/logging/gin_logger.go`

## 备份文件
- `./internal/logging/gin_logger.go.backup`

## 修改内容

### 1. 日志格式改进
**原格式**（带竖线）：
```
INFO | a1b2c3d4 | 200 | 27.21s | 116.26.12.138 | POST "/v1/responses"
```

**新格式**（无竖线，空格分隔）：
```
INFO a1b2c3d4 200 27.21s 116.26.12.138 POST "/v1/responses"
```

### 2. 添加模型显示
当请求包含模型信息时，会自动显示：
```
INFO a1b2c3d4 200 27.21s 116.26.12.138 POST "/v1/responses" [model: claude-sonnet-4-6]
```

### 3. 模型信息获取方式
日志系统会按以下顺序尝试获取模型名称：

1. **从 gin context 读取**（优先级最高）
   - Handler 可调用 `logging.SetModelName(c, "model-name")` 设置模型
   
2. **从请求头读取**（次优先级）
   - `X-Model`
   - `X-Request-Model`
   - `X-Upstream-Model`

### 4. 新增的导出函数
```go
// SetModelName 在 handler 中设置模型名称供日志使用
func SetModelName(c *gin.Context, modelName string)
```

## 使用示例

在任何 handler 中设置模型名称：

```go
import "github.com/router-for-me/CLIProxyAPI/v7/internal/logging"

func SomeHandler(c *gin.Context) {
    // 解析请求，获取模型名称
    modelName := getModelFromRequest(c)
    
    // 设置到 context，日志会自动显示
    logging.SetModelName(c, modelName)
    
    // 继续处理请求...
}
```

## 测试方法

1. 发送一个 AI API 请求到 CPA
2. 查看日志文件或控制台输出
3. 确认格式：
   - ✅ 没有竖线分隔符
   - ✅ RequestID 在前面
   - ✅ 如果有模型信息会显示 `[model: xxx]`

## 回滚方法

如果需要恢复原格式：
```bash
cd /home/div/1_Project_dir/AI/CLIProxyAPI
cp ./internal/logging/gin_logger.go.backup ./internal/logging/gin_logger.go
docker restart cli-proxy-api
```

## 后续工作

如果需要在特定 handler 中显示模型信息，需要：

1. 找到对应的 handler 文件（如 `./internal/api/handlers/openai/*.go`）
2. 在处理请求时调用 `logging.SetModelName(c, modelName)`
3. 重启容器生效

## 注意事项

- ⚠️ 这是 **Docker 热挂载项目**，修改 `.go` 文件后**必须重启容器**
- ⚠️ 不需要重新编译，容器内的二进制文件会自动更新
- ⚠️ 模型信息的显示依赖于 handler 是否调用了 `SetModelName()`

## 日志示例

### 成功请求（无模型信息）
```
INFO ff8eea3e 200      27.21s   116.26.12.138 POST    "/v1/responses"
```

### 成功请求（有模型信息）
```
INFO ff8eea3e 200      27.21s   116.26.12.138 POST    "/v1/responses" [model: claude-sonnet-4-6]
```

### 失败请求
```
WARN a1b2c3d4 429       1.23s   192.168.1.100 POST    "/v1/chat/completions" [model: gpt-4] rate limit exceeded
```

### 错误请求
```
ERROR b2c3d4e5 500      15.67s   10.0.0.50     POST    "/v1/messages" internal server error
```
