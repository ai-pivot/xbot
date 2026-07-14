/**
 * SettingsLLM — Full LLM config panel (Spec D).
 *
 * Structure:
 *   ├── 模型与推理（用户级）
 *   │   ├── Thinking Mode 下拉
 *   │   ├── Max Concurrency 数字输入
 *   │   └── Tier 配置 × 3
 *   ├── 订阅管理
 *   │   ├── 订阅列表（可展开/折叠）
 *   │   │   └── 每个订阅：信息行 + 展开后模型列表 + 编辑表单
 *   │   └── [+ 添加订阅]
 *   └── 当前会话模型（只读）
 */
import { useEffect, useState } from 'react'
import { toast } from 'sonner'
import {
  ChevronDown,
  ChevronRight,
  Loader2,
  Pencil,
  Plus,
  Power,
  RefreshCw,
  Star,
  Trash2,
  Lock,
} from 'lucide-react'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Skeleton } from '@/components/ui/skeleton'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'
import { useI18n } from '@/providers/i18n'
import { useLLMSettings } from '@/hooks/useLLMSettings'
import { isMaskedAPIKey } from '@/components/agent/api'
import type { Subscription, ModelEntry, PerModelConfig } from '@/types/shared'

import { SettingsSection } from './SettingsSection'

interface SettingsLLMProps {
  settings: ReturnType<typeof useLLMSettings>
}

// ── NumberField ──

function NumberField({
  value,
  disabled,
  onCommit,
}: {
  value: number
  disabled?: boolean
  onCommit: (n: number) => Promise<boolean>
}) {
  const { t } = useI18n()
  const [text, setText] = useState(String(value))
  useEffect(() => setText(String(value)), [value])

  const commit = () => {
    const n = Number(text)
    if (!Number.isFinite(n) || n < 0 || n === value) return
    void onCommit(n).then((ok) => {
      toast[ok ? 'success' : 'error'](ok ? t('settings.saved') : t('settings.saveFailed'))
    })
  }

  return (
    <Input
      type="number"
      min={0}
      inputMode="numeric"
      value={text}
      disabled={disabled}
      onChange={(e) => setText(e.target.value)}
      onBlur={commit}
      onKeyDown={(e) => {
        if (e.key === 'Enter') (e.target as HTMLInputElement).blur()
      }}
      className="max-w-[200px]"
    />
  )
}

// ── Subscription Form Data ──

interface SubFormData {
  name: string
  provider: string
  base_url: string
  api_key: string
  model: string
}

function emptySubForm(): SubFormData {
  return { name: '', provider: 'openai', base_url: '', api_key: '', model: '' }
}

function subToForm(sub: Subscription): SubFormData {
  return {
    name: sub.name,
    provider: sub.provider,
    base_url: sub.base_url,
    api_key: sub.api_key,
    model: sub.model,
  }
}

// ── Subscription Edit Form ──

function SubscriptionEditForm({
  initial,
  saving,
  onSave,
  onCancel,
}: {
  initial: SubFormData
  saving: boolean
  onSave: (data: SubFormData) => void
  onCancel: () => void
}) {
  const { t } = useI18n()
  const [form, setForm] = useState(initial)

  const set = (k: keyof SubFormData, v: string) => setForm((f) => ({ ...f, [k]: v }))

  return (
    <div className="flex flex-col gap-3 rounded-md border border-border bg-bg-secondary p-3">
      <div className="flex flex-col gap-1">
        <Label className="text-xs text-muted-foreground">{t('settings.subscriptionName')}</Label>
        <Input
          value={form.name}
          onChange={(e) => set('name', e.target.value)}
          placeholder="My OpenAI"
        />
      </div>
      <div className="flex flex-col gap-1">
        <Label className="text-xs text-muted-foreground">{t('settings.subscriptionProvider')}</Label>
        <Select value={form.provider} onValueChange={(v) => set('provider', v)}>
          <SelectTrigger className="w-full">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="openai">{t('settings.providerOpenAI')}</SelectItem>
            <SelectItem value="openai_responses">{t('settings.providerOpenAIResponses')}</SelectItem>
            <SelectItem value="anthropic">{t('settings.providerAnthropic')}</SelectItem>
          </SelectContent>
        </Select>
      </div>
      <div className="flex flex-col gap-1">
        <Label className="text-xs text-muted-foreground">{t('settings.subscriptionBaseURL')}</Label>
        <Input
          value={form.base_url}
          onChange={(e) => set('base_url', e.target.value)}
          placeholder="https://api.openai.com/v1"
        />
      </div>
      <div className="flex flex-col gap-1">
        <Label className="text-xs text-muted-foreground">{t('settings.subscriptionAPIKey')}</Label>
        <Input
          type="password"
          value={form.api_key}
          onChange={(e) => set('api_key', e.target.value)}
          placeholder="sk-..."
        />
      </div>
      <div className="flex flex-col gap-1">
        <Label className="text-xs text-muted-foreground">{t('settings.subscriptionDefaultModel')}</Label>
        <Input
          value={form.model}
          onChange={(e) => set('model', e.target.value)}
          placeholder="gpt-4o"
        />
      </div>
      <div className="flex gap-2">
        <Button size="sm" disabled={saving} onClick={() => onSave(form)}>
          {saving ? <Loader2 className="size-4 animate-spin" /> : null}
          {t('common.save')}
        </Button>
        <Button size="sm" variant="outline" onClick={onCancel}>
          {t('common.cancel')}
        </Button>
      </div>
    </div>
  )
}

// ── Model Edit Form ──

interface ModelFormData {
  enabled: boolean
  max_output_tokens: number
  max_context: number
  api_type: string
}

function modelToForm(entry: ModelEntry, sub: Subscription | undefined): ModelFormData {
  const pmc = sub?.per_model_configs?.[entry.model]
  return {
    enabled: entry.status !== 'disabled',
    max_output_tokens: pmc?.max_output_tokens ?? 0,
    max_context: pmc?.max_context ?? 0,
    api_type: pmc?.api_type ?? '',
  }
}

function ModelEditForm({
  initial,
  saving,
  onSave,
  onCancel,
}: {
  initial: ModelFormData
  saving: boolean
  onSave: (data: ModelFormData) => void
  onCancel: () => void
}) {
  const { t } = useI18n()
  const [form, setForm] = useState(initial)

  const set = <K extends keyof ModelFormData>(k: K, v: ModelFormData[K]) =>
    setForm((f) => ({ ...f, [k]: v }))

  return (
    <div className="flex flex-col gap-2 rounded-md border border-border bg-bg-tertiary p-2.5">
      <div className="flex items-center gap-2">
        <Label className="text-xs text-muted-foreground w-24">{t('settings.modelAPIType')}</Label>
        <Select
          value={form.api_type || 'default'}
          onValueChange={(v) => set('api_type', v === 'default' ? '' : v)}
        >
          <SelectTrigger className="flex-1 h-8">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="default">{t('settings.apiTypeDefault')}</SelectItem>
            <SelectItem value="chat_completions">{t('settings.apiTypeChatCompletions')}</SelectItem>
            <SelectItem value="responses">{t('settings.apiTypeResponses')}</SelectItem>
          </SelectContent>
        </Select>
      </div>
      <div className="flex items-center gap-2">
        <Label className="text-xs text-muted-foreground w-24">{t('settings.modelMaxOutput')}</Label>
        <Input
          type="number"
          min={0}
          value={String(form.max_output_tokens)}
          onChange={(e) => set('max_output_tokens', Number(e.target.value))}
          className="flex-1 h-8"
        />
      </div>
      <div className="flex items-center gap-2">
        <Label className="text-xs text-muted-foreground w-24">{t('settings.modelMaxContext')}</Label>
        <Input
          type="number"
          min={0}
          value={String(form.max_context)}
          onChange={(e) => set('max_context', Number(e.target.value))}
          className="flex-1 h-8"
        />
      </div>
      <div className="flex gap-2 pt-1">
        <Button size="sm" disabled={saving} onClick={() => onSave(form)}>
          {saving ? <Loader2 className="size-4 animate-spin" /> : null}
          {t('common.save')}
        </Button>
        <Button size="sm" variant="outline" onClick={onCancel}>
          {t('common.cancel')}
        </Button>
      </div>
    </div>
  )
}

// ── Model Row ──

function statusColor(status: ModelEntry['status']): string {
  switch (status) {
    case 'normal':
      return 'bg-green-500'
    case 'offline':
      return 'bg-yellow-500'
    case 'disabled':
      return 'bg-gray-400'
  }
}

interface ModelRowProps {
  entry: ModelEntry
  isActive: boolean
  isSystemSub: boolean
  onEdit: () => void
  onToggleEnabled: () => void
  onRemove: () => void
}

function ModelRow({ entry, isActive, isSystemSub, onEdit, onToggleEnabled, onRemove }: ModelRowProps) {
  const { t } = useI18n()
  const statusLabel =
    entry.status === 'normal'
      ? t('settings.modelStatusNormal')
      : entry.status === 'offline'
        ? t('settings.modelStatusOffline')
        : t('settings.modelStatusDisabled')

  return (
    <div className="group flex items-center gap-2 px-2 py-1.5 hover:bg-accent/5">
      <span className={`size-2 shrink-0 rounded-full ${statusColor(entry.status)}`} />
      <span className="flex-1 truncate text-sm">{entry.model}</span>
      <span className="text-[10px] text-muted-foreground">{statusLabel}</span>
      {isActive && <span className="text-accent text-xs">✓</span>}
      {!isSystemSub && (
        <div className="flex items-center gap-0.5 opacity-0 group-hover:opacity-100 transition-opacity">
          <Button
            variant="ghost"
            size="icon-sm"
            className="size-6"
            onClick={onEdit}
            aria-label={t('settings.editModel')}
          >
            <Pencil className="size-3" />
          </Button>
          <Button
            variant="ghost"
            size="icon-sm"
            className="size-6"
            onClick={onToggleEnabled}
            aria-label={entry.status === 'disabled' ? t('settings.enable') : t('settings.disable')}
          >
            <Power className="size-3" />
          </Button>
          <Button
            variant="ghost"
            size="icon-sm"
            className="size-6 text-destructive hover:text-destructive"
            onClick={onRemove}
            aria-label={t('common.delete')}
          >
            <Trash2 className="size-3" />
          </Button>
        </div>
      )}
    </div>
  )
}

// ── Subscription Row ──

interface SubscriptionRowProps {
  sub: Subscription
  modelEntries: ModelEntry[]
  activeModel: string | undefined
  expanded: boolean
  editingSub: boolean
  editingModel: ModelEntry | null
  saving: boolean
  onToggle: () => void
  onStartEditSub: () => void
  onCancelEditSub: () => void
  onSaveSub: (data: SubFormData) => void
  onSetDefault: () => void
  onToggleEnabled: () => void
  onRemove: () => void
  onStartEditModel: (entry: ModelEntry) => void
  onCancelEditModel: () => void
  onSaveModel: (data: ModelFormData) => void
  onToggleModelEnabled: (entry: ModelEntry) => void
  onRemoveModel: (entry: ModelEntry) => void
  onAddModel: () => void
  isAddingModel: boolean
  onConfirmAddModel: (modelName: string) => void
  onCancelAddModel: () => void
}

function SubscriptionRow({
  sub,
  modelEntries,
  activeModel,
  expanded,
  editingSub,
  editingModel,
  saving,
  onToggle,
  onStartEditSub,
  onCancelEditSub,
  onSaveSub,
  onSetDefault,
  onToggleEnabled,
  onRemove,
  onStartEditModel,
  onCancelEditModel,
  onSaveModel,
  onToggleModelEnabled,
  onRemoveModel,
  onAddModel,
  isAddingModel,
  onConfirmAddModel,
  onCancelAddModel,
}: SubscriptionRowProps) {
  const { t } = useI18n()
  const isSystem = sub.is_system
  const subModels = modelEntries.filter((e) => e.sub_id === sub.id)

  return (
    <Collapsible open={expanded} onOpenChange={onToggle}>
      <div className="group flex items-center gap-2 rounded-md px-2 py-2 hover:bg-accent/5">
        <CollapsibleTrigger asChild>
          <button type="button" className="flex items-center gap-2 flex-1 min-w-0 text-left">
            {expanded ? (
              <ChevronDown className="size-4 shrink-0 text-muted-foreground" />
            ) : (
              <ChevronRight className="size-4 shrink-0 text-muted-foreground" />
            )}
            <span className={`size-2 shrink-0 rounded-full ${
              sub.enabled ? (sub.active ? 'bg-accent' : 'bg-green-500') : 'bg-gray-400'
            }`} />
            <span className="truncate text-sm font-medium">{sub.name}</span>
            <span className="truncate text-xs text-muted-foreground">{sub.provider}</span>
            {isSystem && <Lock className="size-3 text-muted-foreground" />}
            {sub.active && (
              <span className="rounded bg-accent/15 px-1.5 py-0.5 text-[10px] text-accent">
                {t('settings.subscriptionActive')}
              </span>
            )}
          </button>
        </CollapsibleTrigger>
        {!isSystem && (
          <div className="flex items-center gap-0.5 opacity-0 group-hover:opacity-100 transition-opacity">
            <Button
              variant="ghost"
              size="icon-sm"
              className="size-6"
              onClick={onStartEditSub}
              aria-label={t('common.rename')}
            >
              <Pencil className="size-3" />
            </Button>
            {!sub.active && (
              <Button
                variant="ghost"
                size="icon-sm"
                className="size-6"
                onClick={onSetDefault}
                aria-label={t('settings.setAsDefault')}
              >
                <Star className="size-3" />
              </Button>
            )}
            <Button
              variant="ghost"
              size="icon-sm"
              className="size-6"
              onClick={onToggleEnabled}
              aria-label={sub.enabled ? t('settings.disable') : t('settings.enable')}
            >
              <Power className="size-3" />
            </Button>
            <Button
              variant="ghost"
              size="icon-sm"
              className="size-6 text-destructive hover:text-destructive"
              onClick={onRemove}
              aria-label={t('common.delete')}
            >
              <Trash2 className="size-3" />
            </Button>
          </div>
        )}
      </div>
      <CollapsibleContent>
        <div className="ml-6 border-l border-border pl-2">
          {/* Edit subscription form */}
          {editingSub && !isSystem && (
            <div className="py-2">
              <SubscriptionEditForm
                initial={subToForm(sub)}
                saving={saving}
                onCancel={onCancelEditSub}
                onSave={onSaveSub}
              />
            </div>
          )}
          {/* Model list */}
          {subModels.length === 0 ? (
            <p className="px-2 py-1.5 text-xs text-muted-foreground">—</p>
          ) : (
            <div className="flex flex-col">
              {subModels.map((entry) => (
                <div key={`${entry.sub_id}-${entry.model}`}>
                  {editingModel?.model === entry.model ? (
                    <div className="py-1">
                      <ModelEditForm
                        initial={modelToForm(entry, sub)}
                        saving={saving}
                        onCancel={onCancelEditModel}
                        onSave={onSaveModel}
                      />
                    </div>
                  ) : (
                    <ModelRow
                      entry={entry}
                      isActive={entry.model === activeModel}
                      isSystemSub={isSystem}
                      onEdit={() => onStartEditModel(entry)}
                      onToggleEnabled={() => onToggleModelEnabled(entry)}
                      onRemove={() => onRemoveModel(entry)}
                    />
                  )}
                </div>
              ))}
            </div>
          )}
          {/* Add model button / form */}
          {!isSystem && (
            isAddingModel ? (
              <div className="mt-1">
                <AddModelForm
                  saving={saving}
                  onAdd={onConfirmAddModel}
                  onCancel={onCancelAddModel}
                />
              </div>
            ) : (
              <Button
                variant="ghost"
                size="sm"
                className="mt-1 gap-1 text-xs text-muted-foreground"
                onClick={onAddModel}
              >
                <Plus className="size-3" />
                {t('settings.addModel')}
              </Button>
            )
          )}
        </div>
      </CollapsibleContent>
    </Collapsible>
  )
}

// ── Tier Selector ──

function TierSelector({
  label,
  value,
  options,
  disabled,
  onChange,
}: {
  label: string
  value: string
  options: { label: string; value: string }[]
  disabled: boolean
  onChange: (v: string) => void
}) {
  return (
    <div className="flex items-center gap-2">
      <Label className="text-xs text-muted-foreground w-20 shrink-0">{label}</Label>
      <Select value={value} onValueChange={onChange} disabled={disabled}>
        <SelectTrigger className="flex-1 h-8">
          <SelectValue placeholder="—" />
        </SelectTrigger>
        <SelectContent>
          {options.map((opt) => (
            <SelectItem key={opt.value} value={opt.value}>
              {opt.label}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </div>
  )
}

// ── Add Model Form ──

function AddModelForm({
  saving,
  onAdd,
  onCancel,
}: {
  saving: boolean
  onAdd: (modelName: string) => void
  onCancel: () => void
}) {
  const { t } = useI18n()
  const [modelName, setModelName] = useState('')

  return (
    <div className="flex items-center gap-2 rounded-md border border-border bg-bg-tertiary p-2.5">
      <Input
        value={modelName}
        onChange={(e) => setModelName(e.target.value)}
        placeholder={t('settings.modelName')}
        className="flex-1 h-8"
        onKeyDown={(e) => {
          if (e.key === 'Enter' && modelName.trim()) onAdd(modelName)
        }}
      />
      <Button size="sm" disabled={saving || !modelName.trim()} onClick={() => onAdd(modelName)}>
        {saving ? <Loader2 className="size-3 animate-spin" /> : <Plus className="size-3" />}
      </Button>
      <Button size="sm" variant="outline" onClick={onCancel}>
        {t('common.cancel')}
      </Button>
    </div>
  )
}

// ── Main Component ──

export function SettingsLLM({ settings }: SettingsLLMProps) {
  const { t } = useI18n()
  const {
    data,
    loading,
    error,
    saving,
    refreshing,
    reload,
    addSubscription,
    updateSubscription,
    removeSubscription,
    setDefaultSubscription,
    setSubscriptionEnabled,
    updatePerModelConfig,
    setModelEnabled,
    removeModel,
    upsertModel,
    refreshModels,
    setThinkingMode,
    setLLMConcurrency,
    setTier,
  } = settings

  const disabled = saving || !!error

  // State
  const [expandedSubs, setExpandedSubs] = useState<Set<string>>(new Set())
  const [editingSubID, setEditingSubID] = useState<string | null>(null)
  const [editingModel, setEditingModel] = useState<{ subID: string; model: string } | null>(null)
  const [addingSub, setAddingSub] = useState(false)
  const [addingModelForSub, setAddingModelForSub] = useState<string | null>(null)

  const toggleSub = (id: string) => {
    setExpandedSubs((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }

  // Thinking mode
  const [thinking, setThinking] = useState(data.thinkingMode)
  useEffect(() => setThinking(data.thinkingMode), [data.thinkingMode])
  const commitThinking = (mode: string) => {
    if (mode === data.thinkingMode) return
    void setThinkingMode(mode).then((ok) => {
      toast[ok ? 'success' : 'error'](ok ? t('settings.saved') : t('settings.saveFailed'))
    })
  }

  // Tier options: exclude disabled models
  const tierOptions = data.modelEntries
    .filter((e) => e.status !== 'disabled')
    .map((e) => ({
      label: `${e.model} (${e.sub_name})`,
      value: `${e.sub_id}|${e.model}`,
    }))

  // ── Subscription handlers ──

  const handleAddSub = (form: SubFormData) => {
    void addSubscription({
      name: form.name,
      provider: form.provider,
      base_url: form.base_url,
      api_key: form.api_key,
      model: form.model,
    }).then((ok) => {
      toast[ok ? 'success' : 'error'](ok ? t('settings.saved') : t('settings.saveFailed'))
      if (ok) setAddingSub(false)
    })
  }

  const handleSaveSub = (sub: Subscription) => (form: SubFormData) => {
    const apiKeyToSend = isMaskedAPIKey(form.api_key) ? '' : form.api_key
    void updateSubscription(sub.id, {
      name: form.name,
      provider: form.provider,
      base_url: form.base_url,
      api_key: apiKeyToSend,
      model: form.model,
    }).then((ok) => {
      toast[ok ? 'success' : 'error'](ok ? t('settings.saved') : t('settings.saveFailed'))
      if (ok) setEditingSubID(null)
    })
  }

  const handleRemoveSub = (sub: Subscription) => {
    if (sub.is_system) {
      toast.error(t('settings.systemSubscriptionProtected'))
      return
    }
    if (!confirm(t('settings.deleteConfirm'))) return
    void removeSubscription(sub.id).then((ok) => {
      toast[ok ? 'success' : 'error'](ok ? t('settings.saved') : t('settings.saveFailed'))
    })
  }

  const handleSetDefault = (sub: Subscription) => {
    if (sub.is_system) {
      toast.error(t('settings.systemSubscriptionProtected'))
      return
    }
    void setDefaultSubscription(sub.id).then((ok) => {
      toast[ok ? 'success' : 'error'](ok ? t('settings.saved') : t('settings.saveFailed'))
    })
  }

  const handleToggleSubEnabled = (sub: Subscription) => {
    if (sub.is_system) {
      toast.error(t('settings.systemSubscriptionProtected'))
      return
    }
    void setSubscriptionEnabled(sub.id, !sub.enabled).then((ok) => {
      toast[ok ? 'success' : 'error'](ok ? t('settings.saved') : t('settings.saveFailed'))
    })
  }

  // ── Model handlers ──

  const handleStartEditModel = (subID: string, entry: ModelEntry) => {
    setEditingModel({ subID, model: entry.model })
  }

  const handleCancelEditModel = () => setEditingModel(null)

  const handleSaveModel = (sub: Subscription, entry: ModelEntry) => (form: ModelFormData) => {
    // Per protocol.PerModelConfig comment: Enabled is a read-side projection,
    // NOT authoritative on writes. We pass the form value for consistency but
    // the actual enabled state is managed by the separate setModelEnabled call below.
    const config: PerModelConfig = {
      max_output_tokens: form.max_output_tokens,
      max_context: form.max_context,
      api_type: form.api_type,
      enabled: form.enabled,
    }
    void updatePerModelConfig(sub.id, entry.model, config).then(async (ok) => {
      if (!ok) {
        toast.error(t('settings.saveFailed'))
        return
      }
      // Toggle enabled state if changed
      if (entry.status === 'disabled' && form.enabled) {
        await setModelEnabled(sub.id, entry.model, true)
      } else if (entry.status !== 'disabled' && !form.enabled) {
        await setModelEnabled(sub.id, entry.model, false)
      }
      toast.success(t('settings.saved'))
      setEditingModel(null)
    })
  }

  const handleToggleModelEnabled = (sub: Subscription, entry: ModelEntry) => {
    if (sub.is_system) {
      toast.error(t('settings.systemSubscriptionProtected'))
      return
    }
    void setModelEnabled(sub.id, entry.model, entry.status !== 'disabled' ? false : true).then((ok) => {
      toast[ok ? 'success' : 'error'](ok ? t('settings.saved') : t('settings.saveFailed'))
    })
  }

  const handleRemoveModel = (sub: Subscription, entry: ModelEntry) => {
    if (sub.is_system) {
      toast.error(t('settings.systemSubscriptionProtected'))
      return
    }
    void removeModel(sub.id, entry.model).then((ok) => {
      toast[ok ? 'success' : 'error'](ok ? t('settings.saved') : t('settings.saveFailed'))
    })
  }

  const handleAddModel = (subID: string) => (modelName: string) => {
    if (!modelName.trim()) return
    void upsertModel(subID, modelName.trim()).then((ok) => {
      toast[ok ? 'success' : 'error'](ok ? t('settings.saved') : t('settings.saveFailed'))
      if (ok) setAddingModelForSub(null)
    })
  }

  const handleRefresh = () => {
    void refreshModels().then((ok) => {
      toast[ok ? 'success' : 'error'](
        ok ? t('settings.refreshModels') : t('settings.refreshTimeout'),
      )
    })
  }

  return (
    <div className="flex flex-col">
      {/* Model & Inference (user-level) */}
      <SettingsSection title={t('settings.modelInference')}>
        {/* Thinking Mode */}
        <div className="flex items-center gap-2">
          <Label className="text-xs text-muted-foreground w-24 shrink-0">
            {t('settings.thinkingMode')}
          </Label>
          <Select
            value={thinking || 'auto'}
            onValueChange={(v) => {
              const mode = v === 'auto' ? '' : v
              setThinking(mode)
              commitThinking(mode)
            }}
            disabled={disabled}
          >
            <SelectTrigger className="flex-1 h-8">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="auto">{t('settings.thinkingAuto')}</SelectItem>
              <SelectItem value="enabled">{t('settings.thinkingEnabled')}</SelectItem>
              <SelectItem value="disabled">{t('settings.thinkingDisabled')}</SelectItem>
            </SelectContent>
          </Select>
        </div>

        {/* Max Concurrency */}
        <div className="flex items-center gap-2">
          <Label className="text-xs text-muted-foreground w-24 shrink-0">
            {t('settings.maxConcurrency')}
          </Label>
          {loading ? (
            <Skeleton className="h-8 w-[200px]" />
          ) : (
            <NumberField
              value={data.llmConcurrency}
              disabled={disabled}
              onCommit={setLLMConcurrency}
            />
          )}
        </div>
      </SettingsSection>

      {/* Tier Config */}
      <SettingsSection title={t('settings.tierVanguard')} description={t('settings.tierDesc')}>
        {loading ? (
          <Skeleton className="h-8 w-full" />
        ) : (
          <div className="flex flex-col gap-2">
            <TierSelector
              label={t('settings.tierVanguard')}
              value={data.tierVanguard}
              options={tierOptions}
              disabled={disabled}
              onChange={(v) => void setTier('vanguard', v)}
            />
            <TierSelector
              label={t('settings.tierBalance')}
              value={data.tierBalance}
              options={tierOptions}
              disabled={disabled}
              onChange={(v) => void setTier('balance', v)}
            />
            <TierSelector
              label={t('settings.tierSwift')}
              value={data.tierSwift}
              options={tierOptions}
              disabled={disabled}
              onChange={(v) => void setTier('swift', v)}
            />
          </div>
        )}
      </SettingsSection>

      {/* Subscription Management */}
      <SettingsSection
        title={t('settings.subscriptionManagement')}
        description={undefined}
      >
        <div className="flex items-center gap-2 pb-1">
          <span className="text-xs text-muted-foreground">
            {data.subscriptions.length} {t('settings.subscriptionManagement')}
          </span>
          <Button
            variant="ghost"
            size="sm"
            className="h-6 gap-1 text-xs"
            disabled={refreshing || disabled}
            onClick={handleRefresh}
          >
            {refreshing ? (
              <Loader2 className="size-3 animate-spin" />
            ) : (
              <RefreshCw className="size-3" />
            )}
            {refreshing ? t('settings.refreshing') : t('settings.refreshModels')}
          </Button>
        </div>

        {loading ? (
          <div className="flex flex-col gap-2">
            <Skeleton className="h-9 w-full" />
            <Skeleton className="h-9 w-full" />
          </div>
        ) : error && error !== 'not_connected' ? (
          <div className="flex items-center justify-between py-2">
            <span className="text-xs text-destructive">{t('settings.loadFailed')}</span>
            <Button variant="outline" size="sm" onClick={() => void reload()}>
              {t('common.retry')}
            </Button>
          </div>
        ) : (
          <div className="flex flex-col gap-1">
            {data.subscriptions.map((sub) => {
              const isEditingSub = editingSubID === sub.id
              const editingModelEntry = editingModel?.subID === sub.id
                ? data.modelEntries.find(
                    (e) => e.sub_id === sub.id && e.model === editingModel.model,
                  ) ?? null
                : null
              return (
                <SubscriptionRow
                  key={sub.id}
                  sub={sub}
                  modelEntries={data.modelEntries}
                  activeModel={undefined}
                  expanded={expandedSubs.has(sub.id)}
                  editingSub={isEditingSub}
                  editingModel={editingModelEntry}
                  saving={saving}
                  onToggle={() => toggleSub(sub.id)}
                  onStartEditSub={() => setEditingSubID(isEditingSub ? null : sub.id)}
                  onCancelEditSub={() => setEditingSubID(null)}
                  onSaveSub={handleSaveSub(sub)}
                  onSetDefault={() => handleSetDefault(sub)}
                  onToggleEnabled={() => handleToggleSubEnabled(sub)}
                  onRemove={() => handleRemoveSub(sub)}
                  onStartEditModel={(entry) => handleStartEditModel(sub.id, entry)}
                  onCancelEditModel={handleCancelEditModel}
                  onSaveModel={
                    editingModelEntry
                      ? handleSaveModel(sub, editingModelEntry)
                      : () => {}
                  }
                  onToggleModelEnabled={(entry) => handleToggleModelEnabled(sub, entry)}
                  onRemoveModel={(entry) => handleRemoveModel(sub, entry)}
                  onAddModel={() =>
                    setAddingModelForSub(addingModelForSub === sub.id ? null : sub.id)
                  }
                  isAddingModel={addingModelForSub === sub.id}
                  onConfirmAddModel={(modelName) => handleAddModel(sub.id)(modelName)}
                  onCancelAddModel={() => setAddingModelForSub(null)}
                />
              )
            })}

            {/* Add subscription form */}
            {addingSub ? (
              <div className="mt-2">
                <SubscriptionEditForm
                  initial={emptySubForm()}
                  saving={saving}
                  onCancel={() => setAddingSub(false)}
                  onSave={handleAddSub}
                />
              </div>
            ) : (
              <Button
                variant="outline"
                size="sm"
                className="mt-1 gap-1"
                disabled={disabled}
                onClick={() => setAddingSub(true)}
              >
                <Plus className="size-4" />
                {t('settings.addSubscription')}
              </Button>
            )}
          </div>
        )}
      </SettingsSection>

      {/* Error display */}
      {error === 'not_connected' ? (
        <p className="px-5 py-3 text-xs text-muted-foreground">{t('settings.notConnected')}</p>
      ) : null}
    </div>
  )
}
