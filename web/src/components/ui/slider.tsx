import * as React from 'react'
import { Slider as SliderPrimitive } from 'radix-ui'

import { cn } from '@/lib/utils'

function Slider({
  className,
  defaultValue,
  value,
  min = 0,
  max = 100,
  ...props
}: React.ComponentProps<typeof SliderPrimitive.Root>) {
  const values = React.useMemo(
    () => (Array.isArray(value) ? value : Array.isArray(defaultValue) ? defaultValue : [min]),
    [defaultValue, min, value],
  )

  return (
    <SliderPrimitive.Root
      data-slot="slider"
      defaultValue={defaultValue}
      value={value}
      min={min}
      max={max}
      className={cn(
        'relative flex w-full touch-none items-center select-none data-[disabled]:opacity-50',
        className,
      )}
      {...props}
    >
      <SliderPrimitive.Track
        data-slot="slider-track"
        className="relative h-1.5 w-full grow overflow-hidden rounded-full bg-border"
      >
        <SliderPrimitive.Range
          data-slot="slider-range"
          className="absolute h-full bg-accent"
        />
      </SliderPrimitive.Track>
      {values.map((_, index) => (
        <SliderPrimitive.Thumb
          data-slot="slider-thumb"
          key={index}
          className="block size-3.5 shrink-0 rounded-full border-2 border-accent bg-bg-primary shadow-sm outline-none transition-shadow focus-visible:ring-2 focus-visible:ring-accent/40 disabled:pointer-events-none"
        />
      ))}
    </SliderPrimitive.Root>
  )
}

export { Slider }
