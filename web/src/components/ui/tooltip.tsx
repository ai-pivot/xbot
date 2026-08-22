import * as React from "react"
import { Tooltip as TooltipPrimitive, Popover as PopoverPrimitive } from "radix-ui"

import { cn } from "@/lib/utils"
import { useIsTouch } from "@/hooks/useIsMobile"

type CtxValue = { touch: boolean; open: boolean; setOpen: (o: boolean) => void }
const TouchCtx = React.createContext<CtxValue | null>(null)

function useTouchCtx(): CtxValue {
  const ctx = React.useContext(TouchCtx)
  if (!ctx) throw new Error('TooltipTrigger/TooltipContent must be inside <Tooltip>')
  return ctx
}

function TooltipProvider({
  delayDuration = 0,
  ...props
}: React.ComponentProps<typeof TooltipPrimitive.Provider>) {
  return (
    <TooltipPrimitive.Provider
      data-slot="tooltip-provider"
      delayDuration={delayDuration}
      {...props}
    />
  )
}

/**
 * Tooltip — hover on desktop, tap on touch devices.
 *
 * radix-ui Tooltip is hover/focus-based and does not respond to a tap on
 * touch-only devices (no hover state), so tooltips like ContextRing were
 * unopenable on mobile. Here we keep the Tooltip API surface (Tooltip /
 * TooltipTrigger / TooltipContent) but render a click-toggled Popover on touch
 * devices, so every existing tooltip works on mobile with zero caller changes.
 */
function Tooltip({
  children,
  ...props
}: React.ComponentProps<typeof TooltipPrimitive.Root>) {
  const touch = useIsTouch()
  const [open, setOpen] = React.useState(false)

  if (touch) {
    return (
      <PopoverPrimitive.Root open={open} onOpenChange={setOpen} data-slot="tooltip" {...props}>
        <TouchCtx.Provider value={{ touch: true, open, setOpen }}>
          {children}
        </TouchCtx.Provider>
      </PopoverPrimitive.Root>
    )
  }

  return (
    <TooltipPrimitive.Root data-slot="tooltip" {...props}>
      <TouchCtx.Provider value={{ touch: false, open, setOpen }}>
        {children}
      </TouchCtx.Provider>
    </TooltipPrimitive.Root>
  )
}

function TooltipTrigger({
  children,
  ...props
}: React.ComponentProps<typeof TooltipPrimitive.Trigger>) {
  const { touch } = useTouchCtx()
  if (touch) {
    return (
      <PopoverPrimitive.Trigger data-slot="tooltip-trigger" {...props}>
        {children}
      </PopoverPrimitive.Trigger>
    )
  }
  return (
    <TooltipPrimitive.Trigger data-slot="tooltip-trigger" {...props}>
      {children}
    </TooltipPrimitive.Trigger>
  )
}

function TooltipContent({
  className,
  sideOffset = 0,
  children,
  ...props
}: React.ComponentProps<typeof TooltipPrimitive.Content>) {
  const { touch } = useTouchCtx()
  const baseClass = cn(
    "z-50 w-fit rounded-md bg-foreground px-3 py-1.5 text-xs text-balance text-background",
    className,
  )

  if (touch) {
    return (
      <PopoverPrimitive.Portal>
        <PopoverPrimitive.Content
          data-slot="tooltip-content"
          sideOffset={sideOffset}
          collisionPadding={8}
          className={cn(baseClass, "shadow-md")}
          {...props}
        >
          {children}
        </PopoverPrimitive.Content>
      </PopoverPrimitive.Portal>
    )
  }

  return (
    <TooltipPrimitive.Portal>
      <TooltipPrimitive.Content
        data-slot="tooltip-content"
        sideOffset={sideOffset}
        className={cn(
          baseClass,
          "origin-(--radix-tooltip-content-transform-origin) animate-in fade-in-0 zoom-in-95 data-[side=bottom]:slide-in-from-top-2 data-[side=left]:slide-in-from-right-2 data-[side=right]:slide-in-from-left-2 data-[side=top]:slide-in-from-bottom-2 data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=closed]:zoom-out-95",
        )}
        {...props}
      >
        {children}
        <TooltipPrimitive.Arrow className="z-50 size-2.5 translate-y-[calc(-50%_-_2px)] rotate-45 rounded-[2px] bg-foreground fill-foreground" />
      </TooltipPrimitive.Content>
    </TooltipPrimitive.Portal>
  )
}

export { Tooltip, TooltipTrigger, TooltipContent, TooltipProvider }