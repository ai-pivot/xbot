import { useCallback, useEffect, useRef, useState } from 'react'
import { Eye, Loader2, Trash2, Upload } from 'lucide-react'

import { postAPI } from '@/lib/api'
import { useI18n } from '@/providers/i18n'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { ScrollArea } from '@/components/ui/scroll-area'
import { Switch } from '@/components/ui/switch'
import type { TabManager } from '@/hooks/useTabManager'

interface SkillDetail {
  name: string
  description: string
  path: string
  source: string // embedded | global | user | project
  enabled: boolean
  can_uninstall: boolean
  author?: string
  tags?: string[]
}

const SOURCE_VARIANT: Record<string, 'default' | 'secondary' | 'outline' | 'ghost'> = {
  embedded: 'secondary',
  global: 'outline',
  user: 'default',
  project: 'ghost',
}

export function SkillsPanel({ tabManager }: { tabManager: TabManager }) {
  const { t } = useI18n()
  const [skills, setSkills] = useState<SkillDetail[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [viewingSkill, setViewingSkill] = useState<string | null>(null)
  const fileInputRef = useRef<HTMLInputElement>(null)

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const data = await postAPI<SkillDetail[]>('/api/skills/list')
      setSkills(data ?? [])
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    load()
  }, [load])

  const handleToggle = useCallback(
    async (skill: SkillDetail, enabled: boolean) => {
      // Optimistic update
      setSkills((prev) =>
        prev.map((s) => (s.name === skill.name ? { ...s, enabled } : s)),
      )
      try {
        await postAPI('/api/skills/toggle', { name: skill.name, enabled })
      } catch {
        // Revert on failure
        setSkills((prev) =>
          prev.map((s) =>
            s.name === skill.name ? { ...s, enabled: !enabled } : s,
          ),
        )
      }
    },
    [],
  )

  const handleView = useCallback(
    async (skill: SkillDetail) => {
      const isEmbedded = skill.path.startsWith('embedded:')

      if (isEmbedded) {
        // Embedded skills have no file on disk — fetch content and open a
        // read-only tab with virtual content.
        setViewingSkill(skill.name)
        try {
          const res = await postAPI<{ content: string }>('/api/skills/content', {
            path: skill.path,
          })
          tabManager.openTab({
            type: 'file',
            title: `${skill.name} (SKILL.md)`,
            icon: 'BookOpen',
            closable: true,
            data: {
              filePath: `skill://${skill.name}/SKILL.md`,
              content: res.content,
              readOnly: true,
            },
          })
        } catch (e) {
          setError(e instanceof Error ? e.message : String(e))
        } finally {
          setViewingSkill(null)
        }
      } else {
        // Disk skills — open the actual SKILL.md file (editable, like the
        // file explorer).
        const filePath = `${skill.path}/SKILL.md`
        tabManager.openTab({
          type: 'file',
          title: `${skill.name} (SKILL.md)`,
          icon: 'BookOpen',
          closable: true,
          data: { filePath },
        })
      }
    },
    [tabManager],
  )

  const handleUninstall = useCallback(
    async (skill: SkillDetail) => {
      try {
        await postAPI('/api/app/uninstall', {
          type: 'skill',
          name: skill.name,
        })
        setSkills((prev) => prev.filter((s) => s.name !== skill.name))
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e))
      }
    },
    [],
  )

  const handleInstallFile = useCallback(
    async (e: React.ChangeEvent<HTMLInputElement>) => {
      const file = e.target.files?.[0]
      if (!file) return
      const formData = new FormData()
      formData.append('file', file)
      try {
        await postAPI('/api/app/install-file', formData)
        await load()
      } catch (err) {
        setError(err instanceof Error ? err.message : String(err))
      } finally {
        if (fileInputRef.current) fileInputRef.current.value = ''
      }
    },
    [load],
  )

  return (
    <div className="flex h-full flex-col">
      {/* Header */}
      <div className="flex items-center justify-between border-b px-3 py-2">
        <span className="text-sm font-medium">{t('sidebar.skills')}</span>
        <Button
          variant="ghost"
          size="sm"
          onClick={() => fileInputRef.current?.click()}
        >
          <Upload className="size-3.5" />
          {t('skills.install')}
        </Button>
        <input
          ref={fileInputRef}
          type="file"
          accept=".zip,.tar.gz,.tgz"
          className="hidden"
          onChange={handleInstallFile}
        />
      </div>

      {/* Content */}
      <ScrollArea className="flex-1">
        {loading ? (
          <div className="flex items-center justify-center py-8 text-muted-foreground">
            <Loader2 className="size-4 animate-spin" />
          </div>
        ) : error ? (
          <div className="px-3 py-4 text-sm text-destructive">{error}</div>
        ) : skills.length === 0 ? (
          <div className="px-3 py-8 text-center text-sm text-muted-foreground">
            {t('skills.empty')}
          </div>
        ) : (
          <div className="flex flex-col gap-1 p-2">
            {skills.map((skill) => (
              <div
                key={skill.name}
                className="group flex items-start gap-2 rounded-md border border-transparent px-2 py-1.5 hover:border-border hover:bg-accent/50"
              >
                <div className="min-w-0 flex-1">
                  <div className="flex items-center gap-1.5">
                    <span className="truncate text-sm font-medium">
                      {skill.name}
                    </span>
                    <Badge variant={SOURCE_VARIANT[skill.source] ?? 'outline'}>
                      {skill.source}
                    </Badge>
                  </div>
                  {skill.description && (
                    <p className="mt-0.5 line-clamp-2 text-xs text-muted-foreground">
                      {skill.description}
                    </p>
                  )}
                </div>

                {/* Actions */}
                <div className="flex shrink-0 items-center gap-1">
                  <Button
                    variant="ghost"
                    size="icon"
                    className="size-6"
                    onClick={() => handleView(skill)}
                    title={t('skills.view')}
                    disabled={viewingSkill === skill.name}
                  >
                    {viewingSkill === skill.name ? (
                      <Loader2 className="size-3.5 animate-spin" />
                    ) : (
                      <Eye className="size-3.5" />
                    )}
                  </Button>
                  {skill.can_uninstall && (
                    <Button
                      variant="ghost"
                      size="icon"
                      className="size-6 text-muted-foreground hover:text-destructive"
                      onClick={() => handleUninstall(skill)}
                      title={t('skills.uninstall')}
                    >
                      <Trash2 className="size-3.5" />
                    </Button>
                  )}
                  <Switch
                    size="sm"
                    checked={skill.enabled}
                    onCheckedChange={(checked) =>
                      handleToggle(skill, checked)
                    }
                  />
                </div>
              </div>
            ))}
          </div>
        )}
      </ScrollArea>
    </div>
  )
}
