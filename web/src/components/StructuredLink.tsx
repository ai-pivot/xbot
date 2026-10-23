/**
 * 指向 /structured 的链接。
 *
 * 侧栏卡片 / 通信视图空态共用的跳转入口：既可能渲染在 react-router 路由里
 * （AppShell、/court），也可能在单测里裸渲染（没有 Router）——`useInRouterContext()` 在无 Router 时返回
 * false，此时退回普通 <a>，避免为了一个引导链接把整个组件绑死在 Router 上。
 */
import type { ReactNode } from 'react'
import { Link, useInRouterContext } from 'react-router-dom'

export function StructuredLink({
  className,
  children,
  title,
}: {
  className?: string
  children: ReactNode
  title?: string
}) {
  const inRouter = useInRouterContext()
  if (inRouter) {
    return <Link to="/structured" className={className} title={title}>{children}</Link>
  }
  return <a href="/structured" className={className} title={title}>{children}</a>
}
