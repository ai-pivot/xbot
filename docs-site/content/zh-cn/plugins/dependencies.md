---
title: "插件依赖"
weight: 16
---

插件可以声明对其他插件的依赖。依赖解析器使用拓扑排序确定激活顺序。

## 清单声明

```json
{
  "dependencies": [
    {
      "id": "xbot.utils",
      "version": "^1.0.0"
    }
  ]
}
```

| 字段 | 类型 | 描述 |
|------|------|------|
| `id` | string | 必需的插件 ID |
| `version` | string | 版本约束（semver 范围） |

## DependencyResolver

`DependencyResolver`（`plugin/dependency.go`）使用 Kahn 算法（基于 BFS 的拓扑排序），时间复杂度 O(V+E)。

```go
type DependencyResolver struct { ... }

func NewDependencyResolver() *DependencyResolver
func (dr *DependencyResolver) AddManifest(m *PluginManifest)
func (dr *DependencyResolver) Resolve() ([]string, error)
func (dr *DependencyResolver) Validate() error
```

### Resolve

返回激活顺序——无依赖的插件在前，依赖它们的插件在后：

```go
dr := NewDependencyResolver()
dr.AddManifest(manifestA)  // 无依赖
dr.AddManifest(manifestB)  // 依赖 A
dr.AddManifest(manifestC)  // 依赖 A 和 B

order, err := dr.Resolve()
// order = ["xbot.a", "xbot.b", "xbot.c"]
```

### Validate

检查所有已声明的依赖都存在于已添加的清单中。对第一个缺失的依赖返回 `ErrMissingDependency`。

## 错误类型

### ErrMissingDependency

```go
type ErrMissingDependency struct {
    PluginID string  // 存在缺失依赖的插件
    Missing  string  // 缺失的依赖 ID
}
```

插件声明了对不存在插件的依赖时返回。

### ErrCircularDependency

```go
type ErrCircularDependency struct {
    Cycle []string  // 环中的插件 ID
}
```

检测到循环依赖时返回。`Cycle` 字段列出环中涉及的插件。

## 版本约束

版本约束使用 semver 范围语法：

| 约束 | 描述 |
|------|------|
| `1.0.0` | 精确版本 |
| `^1.0.0` | 与 1.0.0 兼容（>=1.0.0, <2.0.0） |
| `>=1.0.0` | 至少 1.0.0 |
| `~1.0.0` | 补丁级兼容（>=1.0.0, <1.1.0） |
| `1.x` | 主版本 1 |
| `*` | 任意版本 |

**注意**：目前只做格式校验。实际的版本解析将在未来迭代中添加。

## 激活顺序

在 `PluginManager.Discover()` 中，所有清单加载完成后：

1. 所有清单被添加到 `DependencyResolver`
2. 调用 `Resolve()` 获取激活顺序
3. `ActivateAll()` 按依赖顺序激活插件
4. 若解析失败（缺失依赖或循环），记录错误但发现流程继续——插件仍会被加载，只是顺序不保证

## 示例

```json
// "xbot.utils" 的 plugin.json
{
  "id": "xbot.utils",
  "name": "Utils",
  "version": "1.0.0",
  "runtime": "native",
  "entry": ""
}

// "xbot.my-plugin" 的 plugin.json
{
  "id": "xbot.my-plugin",
  "name": "My Plugin",
  "version": "1.0.0",
  "runtime": "stdio",
  "entry": "python3 main.py",
  "dependencies": [
    {"id": "xbot.utils", "version": "^1.0.0"}
  ]
}
```

`xbot.utils` 先于 `xbot.my-plugin` 激活。

## 参见

- [插件清单](./manifest/) — 依赖的声明位置
- [架构概览](./architecture/) — 依赖如何影响激活
- [API 参考](./api-reference/) — DependencyResolver API
