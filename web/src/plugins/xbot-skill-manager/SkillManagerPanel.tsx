/**
 * xbot.skill-manager 的视图组件——技能管理面板。
 *
 * 由旧分支 SkillsPanel 移植，API 调用替换为插件运行时核心 RPC：
 * - skill_list / skill_set_enabled / skill_get_content：runtime.rpc.call 无点号直传 /api/rpc
 * - export：薄 REST /api/skills/export（zip 二进制下载）
 * - install / uninstall：复用 master 通用市场 REST（/api/app/install-file + /api/app/uninstall，
 *   后端从会话身份注入 sender_id，不信任前端参数）
 */
import { useCallback, useEffect, useRef, useState } from 'react'
import { useI18n } from '@/providers/i18n'
import { postAPI, postRawAPI } from '@/lib/api'
import { usePluginRuntime } from '@/plugin-runtime'
import { Button } from '@/components/ui/button'
import { MarkdownPreview } from '@/components/file/MarkdownPreview'
import { Loader2, Download, Trash2, Eye, FileUp, ArrowLeft } from 'lucide-react'

/** 与后端 agent/skills.go SkillDetail 对齐。 */
interface SkillDetail {
  name: string
  description: string
  path: string
  author?: string
  tags?: string
  sharing?: string
  source: 'embedded' | 'global' | 'user' | 'project'
  enabled: boolean
  can_uninstall: boolean
}

export function SkillManagerPanel() {
  const runtime = usePluginRuntime()
  const { t } = useI18n()
  const [skills, setSkills] = useState<SkillDetail[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [installing, setInstalling] = useState(false)
  const [viewing, setViewing] = useState<{ name: string; content: string } | null>(null)
  const fileInputRef = useRef<HTMLInputElement>(null)

  const load = useCallback(async () => {
    setLoading(true)
    setError('')
    try {
      const res = (await runtime.rpc.call(
        'skill_list' as never,
        { project_dir: '' } as never,
      )) as unknown as SkillDetail[]
      setSkills(Array.isArray(res) ? res : [])
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setLoading(false)
    }
  }, [runtime])

  useEffect(() => {
    void load()
  }, [load])

  const handleToggle = useCallback(
    async (name: string, enabled: boolean) => {
      try {
        await runtime.rpc.call('skill_set_enabled' as never, { name, enabled } as never)
        await load()
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e))
      }
    },
    [runtime, load],
  )

  const handleView = useCallback(
    async (skill: SkillDetail) => {
      try {
        const res = (await runtime.rpc.call(
          'skill_get_content' as never,
          { path: skill.path } as never,
        )) as unknown as { content?: string }
        setViewing({ name: skill.name, content: res?.content ?? '' })
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e))
      }
    },
    [runtime],
  )

  const handleExport = useCallback(async (skill: SkillDetail) => {
    try {
      const res = await postRawAPI('/api/skills/export', { path: skill.path, name: skill.name })
      const blob = await res.blob()
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      a.download = `${skill.name}.zip`
      a.click()
      URL.revokeObjectURL(url)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }, [])

  const handleUninstall = useCallback(
    async (skill: SkillDetail) => {
      if (!skill.can_uninstall) return
      if (!window.confirm(t('skills.confirmUninstall').replace('{name}', skill.name))) return
      try {
        await postAPI('/api/app/uninstall', { type: 'skill', name: skill.name })
        await load()
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e))
      }
    },
    [load, t],
  )

  const handleInstallFile = useCallback(
    async (file: File | undefined) => {
      if (!file) return
      setInstalling(true)
      setError('')
      try {
        const form = new FormData()
        form.append('file', file)
        await postAPI('/api/app/install-file', form)
        await load()
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e))
      } finally {
        setInstalling(false)
      }
    },
    [load],
  )

  return (
    <div className="flex h-full flex-col">
      <div className="flex items-center justify-between border-b px-3 py-2">
        <span className="text-sm font-medium">{t('sidebar.skills')}</span>
        <Button
          size="sm"
          variant="ghost"
          disabled={installing}
          onClick={() => fileInputRef.current?.click()}
        >
          <FileUp className="mr-1 h-3.5 w-3.5" />
          {installing ? t('skills.installing') : t('skills.install')}
        </Button>
        <input
          ref={fileInputRef}
          type="file"
          accept=".zip"
          className="hidden"
          onChange={(e) => {
            void handleInstallFile(e.target.files?.[0])
            e.target.value = ''
          }}
        />
      </div>

      <div className="flex-1 overflow-y-auto p-2">
        {viewing ? (
          <div className="flex h-full flex-col">
            <div className="mb-2 flex items-center gap-1">
              <Button
                size="sm"
                variant="ghost"
                title={t('skills.back')}
                onClick={() => setViewing(null)}
              >
                <ArrowLeft className="h-3.5 w-3.5" />
              </Button>
              <span className="truncate text-sm font-medium">{viewing.name} (SKILL.md)</span>
            </div>
            <MarkdownPreview source={viewing.content} />
          </div>
        ) : loading ? (
          <div className="flex h-full items-center justify-center text-muted-foreground">
            <Loader2 className="mr-2 h-4 w-4 animate-spin" />
            {t('skills.loading')}
          </div>
        ) : error ? (
          <div className="rounded-md bg-destructive/10 p-2 text-sm text-destructive">{error}</div>
        ) : skills.length === 0 ? (
          <div className="py-8 text-center text-sm text-muted-foreground">{t('skills.empty')}</div>
        ) : (
          <ul className="space-y-1">
            {skills.map((skill) => (
              <li
                key={`${skill.source}:${skill.name}`}
                className="rounded-md border p-2"
              >
                <div className="flex items-center justify-between gap-2">
                  <div className="min-w-0 flex-1">
                    <div className="flex items-center gap-2">
                      <span className="truncate text-sm font-medium">{skill.name}</span>
                      <span className="shrink-0 rounded bg-muted px-1 text-[10px] text-muted-foreground">
                        {skill.source}
                      </span>
                      {!skill.enabled && (
                        <span className="shrink-0 rounded bg-destructive/10 px-1 text-[10px] text-destructive">
                          {t('skills.disabled')}
                        </span>
                      )}
                    </div>
                    {skill.description && (
                      <div className="mt-0.5 line-clamp-2 text-xs text-muted-foreground">
                        {skill.description}
                      </div>
                    )}
                  </div>
                  <div className="flex shrink-0 items-center gap-1">
                    <Button size="sm" variant="ghost" title={t('skills.view')} onClick={() => void handleView(skill)}>
                      <Eye className="h-3.5 w-3.5" />
                    </Button>
                    <Button size="sm" variant="ghost" title={t('skills.export')} onClick={() => void handleExport(skill)}>
                      <Download className="h-3.5 w-3.5" />
                    </Button>
                    {skill.can_uninstall && (
                      <Button
                        size="sm"
                        variant="ghost"
                        title={t('skills.uninstall')}
                        onClick={() => void handleUninstall(skill)}
                      >
                        <Trash2 className="h-3.5 w-3.5" />
                      </Button>
                    )}
                  </div>
                </div>
                {skill.source !== 'embedded' && (
                  <div className="mt-1 flex items-center justify-between">
                    <span className="text-[10px] text-muted-foreground">{skill.path}</span>
                    <button
                      type="button"
                      role="switch"
                      aria-checked={skill.enabled}
                      aria-label={`${skill.name} ${skill.enabled ? 'enabled' : 'disabled'}`}
                      onClick={() => void handleToggle(skill.name, !skill.enabled)}
                      className={`relative h-4 w-7 rounded-full transition-colors ${
                        skill.enabled ? 'bg-primary' : 'bg-muted'
                      }`}
                    >
                      <span
                        className={`absolute top-0.5 h-3 w-3 rounded-full bg-background transition-transform ${
                          skill.enabled ? 'translate-x-3.5' : 'translate-x-0.5'
                        }`}
                      />
                    </button>
                  </div>
                )}
              </li>
            ))}
          </ul>
        )}
      </div>
    </div>
  )
}
