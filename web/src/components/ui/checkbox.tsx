import * as React from "react"

import { cn } from "@/lib/utils"

function Checkbox({
  className,
  checked,
  onCheckedChange,
  id,
  ...props
}: Omit<React.ComponentProps<"button">, "onChange"> & {
  checked?: boolean
  onCheckedChange?: (checked: boolean) => void
}) {
  return (
    <button
      type="button"
      role="checkbox"
      aria-checked={checked ?? false}
      id={id}
      data-slot="checkbox"
      onClick={() => onCheckedChange?.(!checked)}
      className={cn(
        "peer size-4 shrink-0 rounded-sm border border-input shadow-xs transition-colors outline-none",
        "focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50",
        "disabled:pointer-events-none disabled:cursor-not-allowed disabled:opacity-50",
        checked && "border-accent bg-accent text-accent-foreground",
        className
      )}
      {...props}
    >
      {checked && (
        <svg viewBox="0 0 16 16" className="size-3.5 fill-none stroke-current stroke-[2.5] text-white dark:text-black">
          <path d="m3.5 8.5 3 3 6-7" strokeLinecap="round" strokeLinejoin="round" />
        </svg>
      )}
    </button>
  )
}

export { Checkbox }
