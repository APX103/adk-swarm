# Agent Registry

极简的 Agent 注册中心/配置服务。

## 作用

- 集中保存 swarm 中所有 Agent 的 `name`、`url`、`description`、`type`。
- 所有调度 Agent（orchestrator）从这里拉取 endpoints list，动态构建自己的委派工具。
- **新增 Agent 时，只需修改 `endpoints.json` 并调用 `POST /reload`，无需重启任何业务 Agent。**

## API

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/agents` | 列出所有 Agent |
| GET | `/agents/{name}` | 查询单个 Agent |
| POST | `/reload` | 重新加载 `endpoints.json` |
| GET | `/health` | 健康检查 |

## 配置示例

见 [`endpoints.json`](./endpoints.json)。新增 Agent 时追加一条：

```json
{
  "name": "new_agent",
  "url": "http://new_agent:8007",
  "description": "擅长做某事的子 Agent",
  "type": "specialist"
}
```

然后：

```bash
curl -X POST http://localhost:8006/reload
```

所有 orchestrator 会在下次拉取时发现它。
