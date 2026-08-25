---
title: "依赖管理"
weight: 18
---

插件可以声明对其他插件的依赖。`DependencyResolver`（`plugin/dependency.go`）构建依赖图，用 Kahn 算法（BFS 拓扑排序，O(V+E)）计算**激活顺序**。

## 声明依赖

```json
{
  "id": "xbot.ci-status",
  "dependencies": [
    { "id": "xbot.git-fancy", "version": ">=0.3.0" }
  ]
}
```

`PluginDependency`（`plugin/plugin.go:145`）：`ID` + `Version`（semver 范围字符串）。清单加载校验版本格式（`plugin/manifest.go`）；**实际的版本解析计划在未来迭代实现**——目前只强制格式校验与图解析。

## 解析器

```go
dr := plugin.NewDependencyResolver()
dr.AddManifest(manifestA) // 重复 ID 替换已有条目
dr.AddManifest(manifestB)
order, err := dr.Resolve() // []string，激活顺序的插件 ID
```

行为（`plugin/dependency.go:48`）：

1. 由 `m.Dependencies` 构建入度表 + 邻接表。
2. 所有入度为 0 的节点入队（排序保证确定性）。
3. BFS：输出插件、递减依赖者入度、新释放节点入队（保持有序）。
4. **环检测**：输出插件数少于添加数时，剩余入度>0 的集合报告为 `ErrCircularDependency{Cycle}`。
5. **缺失依赖**：依赖引用了未添加的插件返回 `ErrMissingDependency{PluginID, Missing}`。

`Validate()` 只检查存在性，返回第一个缺失依赖。

## 与管理器集成

`PluginManager.Discover`（`plugin/manager.go:511`）加载清单后调用 `resolveActivationOrder()`。失败**不使发现失败**——插件照常加载，只是不保证顺序，错误记入日志。`ActivateAll` 按拓扑顺序执行：依赖先于依赖者激活。

## 错误类型

```go
type ErrCircularDependency struct{ Cycle []string }
type ErrMissingDependency struct{ PluginID, Missing string }
```

两者都实现 `error`；可用 `errors.As`/`errors.Is` 做类型化处理。

## 实用模式

```json
// 组件插件依赖数据源插件
{
  "id": "xbot.ci-panel",
  "dependencies": [ { "id": "xbot.git-fancy", "version": "1.0.0" } ]
}
```

推荐实践：

- **反向 DNS 风格的 ID** 让依赖无歧义（`xbot.git-fancy`，而非 `git-fancy`）。
- **固定最低版本**，即使强制是未来工作——清单是给人读的，也是给未来版本读的。
- **不要制造环**——连 A→B→A 这种"软"环都会导致解析失败。把共享逻辑抽成双方都依赖的公共插件。
- **缺失依赖在发现期是软失败**——若插件强制依赖另一个插件，在 `Activate` 中自行验证（例如检查管理器或事件总线），失败时给出清晰错误。
- 依赖顺序是**确定性的**（有序队列）——对测试的可复现启动很有用。
