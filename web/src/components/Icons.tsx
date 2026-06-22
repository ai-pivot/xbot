/**
 * Minimalist SVG icon components — clean 20×20 stroke icons.
 * Usage: <IconReply />, <IconTrash />, etc.
 */
import { memo, type SVGProps } from 'react'

type IconProps = SVGProps<SVGSVGElement>
const s = { width: 16, height: 16, viewBox: '0 0 24 24', fill: 'none', stroke: 'currentColor', strokeWidth: 1.8, strokeLinecap: 'round' as const, strokeLinejoin: 'round' as const }

export const IconReply = memo((props: IconProps) => (
  <svg {...s} {...props}><path d="M9 17 4 12l5-5"/><path d="M20 18v-2a4 4 0 0 0-4-4H4"/></svg>
))
export const IconTrash = memo((props: IconProps) => (
  <svg {...s} {...props}><path d="M3 6h18"/><path d="M8 6V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"/><path d="M19 6-.5 14a2 2 0 0 1-2 2H7.5a2 2 0 0 1-2-2L5 6"/><path d="M10 11v5"/><path d="M14 11v5"/></svg>
))
export const IconRefresh = memo((props: IconProps) => (
  <svg {...s} {...props}><path d="M3.5 12a8.5 8.5 0 0 1 14.8-5.7"/><path d="M21.5 12a8.5 8.5 0 0 1-14.8 5.7"/><polyline points="21.5 3 21.5 7 17.5 7"/><polyline points="3.5 21 3.5 17 7.5 17"/></svg>
))
export const IconCopy = memo((props: IconProps) => (
  <svg {...s} {...props}><rect x="8" y="8" width="14" height="14" rx="2.5"/><path d="M4 16H3a1.5 1.5 0 0 1-1.5-1.5V3A1.5 1.5 0 0 1 3 1.5h11.5A1.5 1.5 0 0 1 16 3v1"/></svg>
))
export const IconChat = memo((props: IconProps) => (
  <svg {...s} {...props}><path d="M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2z"/></svg>
))
export const IconSearch = memo((props: IconProps) => (
  <svg {...s} {...props}><circle cx="11" cy="11" r="8"/><path d="m21 21-4.35-4.35"/></svg>
))
export const IconSettings = memo((props: IconProps) => (
  <svg {...s} {...props}><circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-4 0v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06A1.65 1.65 0 0 0 4.68 15a1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1 0-4h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06A1.65 1.65 0 0 0 9 4.68a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06A1.65 1.65 0 0 0 19.4 9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 0 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1z"/></svg>
))
export const IconSparkles = memo((props: IconProps) => (
  <svg {...s} {...props}><path d="m12 3-1.9 5.8a2 2 0 0 1-1.3 1.3L3 12l5.8 1.9a2 2 0 0 1 1.3 1.3L12 21l1.9-5.8a2 2 0 0 1 1.3-1.3L21 12l-5.8-1.9a2 2 0 0 1-1.3-1.3Z"/></svg>
))
export const IconHelp = memo((props: IconProps) => (
  <svg {...s} {...props}><circle cx="12" cy="12" r="10"/><path d="M9.09 9a3 3 0 0 1 5.83 1c0 2-3 3-3 3"/><path d="M12 17h.01"/></svg>
))
export const IconPaperclip = memo((props: IconProps) => (
  <svg {...s} {...props}><path d="m21.44 11.05-9.19 9.19a6 6 0 0 1-8.49-8.49l8.57-8.57A4 4 0 1 1 18 8.84l-8.59 8.57a2 2 0 0 1-2.83-2.83l8.49-8.48"/></svg>
))
export const IconKeyboard = memo((props: IconProps) => (
  <svg {...s} {...props}><rect x="2" y="4" width="20" height="16" rx="2"/><path d="M6 8h.01M10 8h.01M14 8h.01M18 8h.01M8 12h.01M12 12h.01M16 12h.01M7 16h10"/></svg>
))
export const IconSend = memo((props: IconProps) => (
  <svg {...s} {...props}><path d="m22 2-7 20-4-9-9-4z"/><path d="m22 2-11 11"/></svg>
))
export const IconBolt = memo((props: IconProps) => (
  <svg {...s} {...props}><path d="M13 2 3 14h9l-1 8 10-12h-9l1-8z"/></svg>
))
export const IconList = memo((props: IconProps) => (
  <svg {...s} {...props}><path d="M8 6h13M8 12h13M8 18h13M3 6h.01M3 12h.01M3 18h.01"/></svg>
))

/* ── Status / Feedback ── */
export const IconCheck = memo((props: IconProps) => (
  <svg {...s} {...props}><polyline points="20 6 9 17 4 12"/></svg>
))
export const IconX = memo((props: IconProps) => (
  <svg {...s} {...props}><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg>
))
export const IconAlert = memo((props: IconProps) => (
  <svg {...s} {...props}><path d="M10.29 3.86 1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z"/><line x1="12" y1="9" x2="12" y2="13"/><line x1="12" y1="17" x2="12.01" y2="17"/></svg>
))
export const IconClock = memo((props: IconProps) => (
  <svg {...s} {...props}><circle cx="12" cy="12" r="10"/><polyline points="12 6 12 12 16 14"/></svg>
))
export const IconLightbulb = memo((props: IconProps) => (
  <svg {...s} {...props}><path d="M9 18h6M10 22h4"/><path d="M15.09 14c.18-.98.65-1.74 1.41-2.5A4.65 4.65 0 0 0 18 8 6 6 0 0 0 6 8c0 1 .23 2.23 1.5 3.5A4.61 4.61 0 0 1 8.91 14"/></svg>
))
export const IconPackage = memo((props: IconProps) => (
  <svg {...s} {...props}><line x1="16.5" y1="9.4" x2="7.5" y2="4.21"/><path d="M21 16V8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16z"/><polyline points="3.27 6.96 12 12.01 20.73 6.96"/><line x1="12" y1="22.08" x2="12" y2="12"/></svg>
))
export const IconZap = memo((props: IconProps) => (
  <svg {...s} {...props}><polygon points="13 2 3 14 12 14 11 22 21 10 12 10 13 2"/></svg>
))
export const IconUser = memo((props: IconProps) => (
  <svg {...s} {...props}><path d="M20 21v-2a4 4 0 0 0-4-4H8a4 4 0 0 0-4 4v2"/><circle cx="12" cy="7" r="4"/></svg>
))
export const IconBot = memo((props: IconProps) => (
  <svg {...s} {...props}><rect x="3" y="11" width="18" height="10" rx="2"/><circle cx="12" cy="5" r="2"/><path d="M12 7v4"/><line x1="8" y1="16" x2="8" y2="16"/><line x1="16" y1="16" x2="16" y2="16"/></svg>
))
export const IconBell = memo((props: IconProps) => (
  <svg {...s} {...props}><path d="M18 8A6 6 0 0 0 6 8c0 7-3 9-3 9h18s-3-2-3-9"/><path d="M13.73 21a2 2 0 0 1-3.46 0"/></svg>
))
export const IconBookmark = memo((props: IconProps) => (
  <svg {...s} {...props}><path d="M19 21l-7-5-7 5V5a2 2 0 0 1 2-2h10a2 2 0 0 1 2 2z"/></svg>
))
export const IconVolume = memo((props: IconProps) => (
  <svg {...s} {...props}><polygon points="11 5 6 9 2 9 2 15 6 15 11 19 11 5"/><path d="M19.07 4.93a10 10 0 0 1 0 14.14M15.54 8.46a5 5 0 0 1 0 7.07"/></svg>
))
export const IconVolumeX = memo((props: IconProps) => (
  <svg {...s} {...props}><polygon points="11 5 6 9 2 9 2 15 6 15 11 19 11 5"/><line x1="23" y1="9" x2="17" y2="15"/><line x1="17" y1="9" x2="23" y2="15"/></svg>
))
export const IconMaximize = memo((props: IconProps) => (
  <svg {...s} {...props}><polyline points="15 3 21 3 21 9"/><polyline points="9 21 3 21 3 15"/><line x1="21" y1="3" x2="14" y2="10"/><line x1="3" y1="21" x2="10" y2="14"/></svg>
))
export const IconMinimize = memo((props: IconProps) => (
  <svg {...s} {...props}><polyline points="4 14 10 14 10 20"/><polyline points="20 10 14 10 14 4"/><line x1="14" y1="10" x2="21" y2="3"/><line x1="3" y1="21" x2="10" y2="14"/></svg>
))
export const IconUpload = memo((props: IconProps) => (
  <svg {...s} {...props}><path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/><polyline points="17 8 12 3 7 8"/><line x1="12" y1="3" x2="12" y2="15"/></svg>
))
export const IconDownload = memo((props: IconProps) => (
  <svg {...s} {...props}><path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/><polyline points="7 10 12 15 17 10"/><line x1="12" y1="15" x2="12" y2="3"/></svg>
))
export const IconSave = memo((props: IconProps) => (
  <svg {...s} {...props}><path d="M19 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h11l5 5v11a2 2 0 0 1-2 2z"/><polyline points="17 21 17 13 7 13 7 21"/><polyline points="7 3 7 8 15 8"/></svg>
))
export const IconEdit = memo((props: IconProps) => (
  <svg {...s} {...props}><path d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7"/><path d="M18.5 2.5a2.121 2.121 0 0 1 3 3L12 15l-4 1 1-4 9.5-9.5z"/></svg>
))
export const IconPlus = memo((props: IconProps) => (
  <svg {...s} {...props}><line x1="12" y1="5" x2="12" y2="19"/><line x1="5" y1="12" x2="19" y2="12"/></svg>
))
export const IconPin = memo((props: IconProps) => (
  <svg {...s} {...props}><line x1="12" y1="17" x2="12" y2="22"/><path d="M5 17h14v-1.76a2 2 0 0 0-1.11-1.79l-1.78-.9A2 2 0 0 1 15 10.76V6h1a2 2 0 0 0 0-4H8a2 2 0 0 0 0 4h1v4.76a2 2 0 0 1-1.11 1.79l-1.78.9A2 2 0 0 0 5 15.24z"/></svg>
))
export const IconInbox = memo((props: IconProps) => (
  <svg {...s} {...props}><polyline points="22 12 16 12 14 15 10 15 8 12 2 12"/><path d="M5.45 5.11L2 12v6a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2v-6l-3.45-6.89A2 2 0 0 0 16.76 4H7.24a2 2 0 0 0-1.79 1.11z"/></svg>
))
export const IconThinking = memo((props: IconProps) => (
  <svg {...s} {...props}><circle cx="12" cy="12" r="10"/><path d="M9.09 9a3 3 0 0 1 5.83 1c0 2-3 3-3 3"/><circle cx="12" cy="17" r="0.5"/></svg>
))
export const IconBrain = memo((props: IconProps) => (
  <svg {...s} {...props}><path d="M12 2a7 7 0 0 0-5 2.1A5 5 0 0 0 2 9c0 2.8 2 5 4.5 5.5L12 22l5.5-7.5C20 14 22 11.8 22 9a5 5 0 0 0-5-4.9A7 7 0 0 0 12 2z"/><circle cx="12" cy="9" r="2"/></svg>
))
export const IconPalette = memo((props: IconProps) => (
  <svg {...s} {...props}><circle cx="13.5" cy="6.5" r="2"/><circle cx="17.5" cy="10.5" r="2"/><circle cx="8.5" cy="7.5" r="2"/><circle cx="6.5" cy="12" r="2"/><path d="M12 2C6.5 2 2 6.5 2 12s4.5 10 10 10c.93 0 1.5-.67 1.5-1.5 0-.4-.15-.76-.42-1.06-.26-.3-.42-.66-.42-1.06 0-.83.67-1.5 1.5-1.5H16c3.31 0 6-2.69 6-6 0-5.5-4.5-9.88-10-9.88z"/></svg>
))
export const IconStore = memo((props: IconProps) => (
  <svg {...s} {...props}><path d="M3 9l1-4h16l1 4"/><path d="M3 9v9a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2V9"/><path d="M9 22V9"/><path d="M15 22V9"/><path d="M3 9h18"/></svg>
))
export const IconSidebarCollapse = memo((props: IconProps) => (
  <svg {...s} {...props}><rect x="3" y="3" width="18" height="18" rx="2"/><line x1="9" y1="3" x2="9" y2="21"/><polyline points="14 9 12 12 14 15"/></svg>
))
export const IconSidebarExpand = memo((props: IconProps) => (
  <svg {...s} {...props}><rect x="3" y="3" width="18" height="18" rx="2"/><line x1="15" y1="3" x2="15" y2="21"/><polyline points="10 9 12 12 10 15"/></svg>
))
export const IconLogout = memo((props: IconProps) => (
  <svg {...s} {...props}><path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4"/><polyline points="16 17 21 12 16 7"/><line x1="21" y1="12" x2="9" y2="12"/></svg>
))
export const IconSun = memo((props: IconProps) => (
  <svg {...s} {...props}><circle cx="12" cy="12" r="5"/><line x1="12" y1="1" x2="12" y2="3"/><line x1="12" y1="21" x2="12" y2="23"/><line x1="4.22" y1="4.22" x2="5.64" y2="5.64"/><line x1="18.36" y1="18.36" x2="19.78" y2="19.78"/><line x1="1" y1="12" x2="3" y2="12"/><line x1="21" y1="12" x2="23" y2="12"/><line x1="4.22" y1="19.78" x2="5.64" y2="18.36"/><line x1="18.36" y1="5.64" x2="19.78" y2="4.22"/></svg>
))
export const IconMoon = memo((props: IconProps) => (
  <svg {...s} {...props}><path d="M21 12.79A9 9 0 1 1 11.21 3 7 7 0 0 0 21 12.79z"/></svg>
))
export const IconGlobe = memo((props: IconProps) => (
  <svg {...s} {...props}><circle cx="12" cy="12" r="10"/><line x1="2" y1="12" x2="22" y2="12"/><path d="M12 2a15.3 15.3 0 0 1 4 10 15.3 15.3 0 0 1-4 10 15.3 15.3 0 0 1-4-10 15.3 15.3 0 0 1 4-10z"/></svg>
))
export const IconChevronDown = memo((props: IconProps) => (
  <svg {...s} {...props}><polyline points="6 9 12 15 18 9"/></svg>
))
export const IconFolder = memo((props: IconProps) => (
  <svg {...s} {...props}><path d="M4 20h16a2 2 0 0 0 2-2V8a2 2 0 0 0-2-2h-7.93a2 2 0 0 1-1.66-.9l-.82-1.2A2 2 0 0 0 7.93 3H4a2 2 0 0 0-2 2v13c0 1.1.9 2 2 2z"/></svg>
))